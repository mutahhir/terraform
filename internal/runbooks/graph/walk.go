package runbookgraph

import (
	"fmt"
	"sync"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/dag"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type walkOperation string

const (
	walkOperationPlan    walkOperation = "plan"
	walkOperationExecute walkOperation = "execute"
)

type GraphNodeExecutable interface {
	Execute(*EvalContext, walkOperation) tfdiags.Diagnostics
}

type GraphNodeDynamicExpandable interface {
	DynamicExpand(*EvalContext) (*terraform.Graph, tfdiags.Diagnostics)
}

func walkGraph(graph *terraform.Graph, ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	var graphMu sync.Mutex
	return walkGraphVertices(graph, ctx, op, nil, &graphMu)
}

func walkGraphVertices(graph *terraform.Graph, ctx *EvalContext, op walkOperation, allowed map[dag.Vertex]struct{}, graphMu *sync.Mutex) tfdiags.Diagnostics {
	if graph == nil {
		return nil
	}
	var visited sync.Map
	callback := func(vertex dag.Vertex) tfdiags.Diagnostics {
		if allowed != nil {
			if _, ok := allowed[vertex]; !ok {
				return nil
			}
		}
		if dag.VertexName(vertex) == "root" {
			return nil
		}
		if _, loaded := visited.LoadOrStore(vertex, struct{}{}); loaded {
			return nil
		}
		if op != walkOperationExecute {
			graphMu.Lock()
			rewireExactStepOutputReferences(graph, vertex)
			graphMu.Unlock()
		}
		if expandable, ok := vertex.(GraphNodeDynamicExpandable); ok {
			if op == walkOperationExecute {
				return nil
			}
			if shouldSkipVertex(graph, ctx, vertex) {
				markVertexSkipped(ctx, vertex, graph)
				return nil
			}
			expanded, expandDiags := expandable.DynamicExpand(ctx)
			if expandDiags.HasErrors() {
				return expandDiags
			}
			graphMu.Lock()
			subsumeExpandedGraph(graph, expanded)
			for _, expandedVertex := range expanded.Vertices() {
				if dag.VertexName(expandedVertex) == "root" {
					continue
				}
				if op != walkOperationExecute {
					rewireExactStepOutputReferences(graph, expandedVertex)
				}
			}
			graphMu.Unlock()
			return walkGraphVertices(graph, ctx, op, vertexSet(expanded.Vertices()), graphMu)
		}
		executable, ok := vertex.(GraphNodeExecutable)
		if !ok {
			return nil
		}
		if shouldSkipVertex(graph, ctx, vertex) {
			markVertexSkipped(ctx, vertex, graph)
			return nil
		}
		return executable.Execute(ctx, op)
	}

	diags := graph.AcyclicGraph.Walk(callback)

	if op == walkOperationPlan {
		for _, vertex := range graph.TopologicalOrder() {
			if allowed != nil {
				if _, ok := allowed[vertex]; !ok {
					continue
				}
			}
			if dag.VertexName(vertex) == "root" {
				continue
			}
			if _, ok := stepNameForVertex(vertex); !ok {
				continue
			}
			step, ok := stepForVertex(ctx, vertex)
			if !ok {
				continue
			}
			if step.Status == runbookruntime.StepStatusPlanned {
				setStepStatusForVertex(ctx, vertex, runbookruntime.StepStatusCompleted, "")
			}
		}
	}

	return diags
}

func vertexSet(vertices []dag.Vertex) map[dag.Vertex]struct{} {
	if len(vertices) == 0 {
		return nil
	}
	ret := make(map[dag.Vertex]struct{}, len(vertices))
	for _, vertex := range vertices {
		ret[vertex] = struct{}{}
	}
	return ret
}

func subsumeExpandedGraph(parent, expanded *terraform.Graph) {
	if parent == nil || expanded == nil {
		return
	}
	parent.Subsume(&expanded.AcyclicGraph.Graph)
}

func rewireExactStepOutputReferences(graph *terraform.Graph, vertex dag.Vertex) {
	if graph == nil || vertex == nil {
		return
	}
	refs := referencesForVertex(vertex)
	if len(refs) == 0 {
		return
	}
	currentStep, _ := stepNameForVertex(vertex)
	for _, ref := range refs {
		stepName := ""
		var targets []*NodeStepOutput
		switch r := ref.(type) {
		case runbookaddrs.StepOutput:
			stepName = r.Step.StepName
			if stepName == "" || stepName == currentStep {
				continue
			}
			targets = matchingStepOutputVertices(graph, r)
		case runbookaddrs.Step:
			stepName = r.Step.StepName
			if stepName == "" || stepName == currentStep {
				continue
			}
			targets = allStepOutputVertices(graph, r.Step)
		default:
			continue
		}
		if len(targets) == 0 {
			continue
		}
		graph.RemoveEdge(dag.BasicEdge(vertex, &NodeExpandStep{StepName: stepName}))
		for _, target := range targets {
			graph.Connect(dag.BasicEdge(vertex, target))
		}
	}
}

func referencesForVertex(vertex dag.Vertex) []runbookaddrs.Referenceable {
	switch node := vertex.(type) {
	case *NodeExpandStep:
		if node == nil || node.Config == nil {
			return nil
		}
		return referencesForStep(node.Config)
	case *NodeStepAction:
		return referencesForStepAction(node.Action)
	case *NodeStepData:
		return referencesForStepResource(node.Data)
	case *NodeStepList:
		return referencesForStepResource(node.List)
	case *NodeStepLocal:
		return referencesForStepLocal(node.Local)
	case *NodeStepExecution:
		return referencesForStepExecution(node.Execution)
	case *NodeStepCondition:
		return referencesForStepCondition(node.Condition)
	case *NodeStepOutput:
		return referencesForStepOutput(node.Output)
	default:
		return nil
	}
}

func matchingStepOutputVertices(graph *terraform.Graph, ref runbookaddrs.StepOutput) []*NodeStepOutput {
	if graph == nil || ref.Step.StepName == "" {
		return nil
	}
	var ret []*NodeStepOutput
	for _, vertex := range graph.Vertices() {
		node, ok := vertex.(*NodeStepOutput)
		if !ok || node.Step == nil || node.Output == nil {
			continue
		}
		if node.Step.StepName != ref.Step.StepName || node.Output.Name != ref.OutputName {
			continue
		}
		if ref.Step.InstanceKey != nil && ref.Step.InstanceKey != terraformaddrs.NoKey {
			if node.Step.InstanceKey != ref.Step.InstanceKey {
				continue
			}
		}
		ret = append(ret, node)
	}
	return ret
}

func allStepOutputVertices(graph *terraform.Graph, step runbookaddrs.StepInstance) []*NodeStepOutput {
	if graph == nil || step.StepName == "" {
		return nil
	}
	var ret []*NodeStepOutput
	for _, vertex := range graph.Vertices() {
		node, ok := vertex.(*NodeStepOutput)
		if !ok || node.Step == nil || node.Output == nil {
			continue
		}
		if node.Step.StepName != step.StepName {
			continue
		}
		if step.InstanceKey != nil && step.InstanceKey != terraformaddrs.NoKey && node.Step.InstanceKey != step.InstanceKey {
			continue
		}
		ret = append(ret, node)
	}
	return ret
}

func shouldSkipVertex(graph *terraform.Graph, ctx *EvalContext, vertex dag.Vertex) bool {
	stepName, ok := stepNameForVertex(vertex)
	if !ok {
		return false
	}
	if hasDependencyStateForVertex(ctx, vertex, runbookruntime.StepStatusSkipped, runbookruntime.StepStatusFailed) {
		return true
	}
	for _, dep := range graph.DownEdges(vertex) {
		depStep, ok := stepNameForVertex(dep)
		if !ok || depStep == stepName {
			continue
		}
		if hasDependencyStateForVertex(ctx, dep, runbookruntime.StepStatusSkipped, runbookruntime.StepStatusFailed) {
			return true
		}
	}
	return false
}

func markVertexSkipped(ctx *EvalContext, vertex dag.Vertex, graph *terraform.Graph) {
	stepName, ok := stepNameForVertex(vertex)
	if !ok {
		return
	}
	for _, dep := range graph.DownEdges(vertex) {
		depStep, ok := stepNameForVertex(dep)
		if ok && depStep != stepName {
			setStepStatusForVertex(ctx, vertex, runbookruntime.StepStatusSkipped, fmt.Sprintf("dependency step %q did not complete", depStep))
			return
		}
	}
	setStepStatusForVertex(ctx, vertex, runbookruntime.StepStatusSkipped, "step did not execute")
}

func stepForVertex(ctx *EvalContext, vertex dag.Vertex) (*runbookruntime.Step, bool) {
	switch node := vertex.(type) {
	case *NodeStepInstance:
		return ctx.stepWithKey(node.StepName, node.InstanceKey)
	case *NodeStepAction:
		if node.Step == nil {
			return nil, false
		}
		return ctx.stepWithKey(node.Step.StepName, node.Step.InstanceKey)
	case *NodeStepData:
		if node.Step == nil {
			return nil, false
		}
		return ctx.stepWithKey(node.Step.StepName, node.Step.InstanceKey)
	case *NodeStepList:
		if node.Step == nil {
			return nil, false
		}
		return ctx.stepWithKey(node.Step.StepName, node.Step.InstanceKey)
	case *NodeStepLocal:
		if node.Step == nil {
			return nil, false
		}
		return ctx.stepWithKey(node.Step.StepName, node.Step.InstanceKey)
	case *NodeStepExecution:
		if node.Step == nil {
			return nil, false
		}
		return ctx.stepWithKey(node.Step.StepName, node.Step.InstanceKey)
	case *NodeStepCondition:
		if node.Step == nil {
			return nil, false
		}
		return ctx.stepWithKey(node.Step.StepName, node.Step.InstanceKey)
	case *NodeStepOutput:
		if node.Step == nil {
			return nil, false
		}
		return ctx.stepWithKey(node.Step.StepName, node.Step.InstanceKey)
	default:
		stepName, ok := stepNameForVertex(vertex)
		if !ok {
			return nil, false
		}
		return ctx.Step(stepName)
	}
}

func setStepStatusForVertex(ctx *EvalContext, vertex dag.Vertex, status runbookruntime.StepStatus, reason string) {
	switch node := vertex.(type) {
	case *NodeStepInstance:
		ctx.setStepStatusWithKey(node.StepName, node.InstanceKey, status, reason)
	case *NodeStepAction:
		if node.Step != nil {
			ctx.setStepStatusWithKey(node.Step.StepName, node.Step.InstanceKey, status, reason)
		}
	case *NodeStepData:
		if node.Step != nil {
			ctx.setStepStatusWithKey(node.Step.StepName, node.Step.InstanceKey, status, reason)
		}
	case *NodeStepList:
		if node.Step != nil {
			ctx.setStepStatusWithKey(node.Step.StepName, node.Step.InstanceKey, status, reason)
		}
	case *NodeStepLocal:
		if node.Step != nil {
			ctx.setStepStatusWithKey(node.Step.StepName, node.Step.InstanceKey, status, reason)
		}
	case *NodeStepExecution:
		if node.Step != nil {
			ctx.setStepStatusWithKey(node.Step.StepName, node.Step.InstanceKey, status, reason)
		}
	case *NodeStepCondition:
		if node.Step != nil {
			ctx.setStepStatusWithKey(node.Step.StepName, node.Step.InstanceKey, status, reason)
		}
	case *NodeStepOutput:
		if node.Step != nil {
			ctx.setStepStatusWithKey(node.Step.StepName, node.Step.InstanceKey, status, reason)
		}
	default:
		if stepName, ok := stepNameForVertex(vertex); ok {
			ctx.SetStepStatus(stepName, status, reason)
		}
	}
}

func hasDependencyStateForVertex(ctx *EvalContext, vertex dag.Vertex, statuses ...runbookruntime.StepStatus) bool {
	step, ok := stepForVertex(ctx, vertex)
	if !ok {
		return false
	}
	for _, status := range statuses {
		if step.Status == status {
			return true
		}
	}
	return false
}

func stepNameForVertex(vertex dag.Vertex) (string, bool) {
	switch node := vertex.(type) {
	case *NodeExpandStep:
		return node.StepName, true
	case *NodeStepInstance:
		return node.StepName, true
	case *NodeStepAction:
		return node.Step.StepName, node.Step != nil
	case *NodeStepData:
		return node.Step.StepName, node.Step != nil
	case *NodeStepList:
		return node.Step.StepName, node.Step != nil
	case *NodeStepLocal:
		return node.Step.StepName, node.Step != nil
	case *NodeStepExecution:
		return node.Step.StepName, node.Step != nil
	case *NodeStepCondition:
		return node.Step.StepName, node.Step != nil
	case *NodeStepOutput:
		return node.Step.StepName, node.Step != nil
	default:
		return "", false
	}
}
