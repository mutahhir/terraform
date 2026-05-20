package runbookgraph

import (
	"fmt"

	"github.com/hashicorp/terraform/internal/dag"
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

// walkGraph walks the given graph, executing nodes according to the operation.
// For expandable nodes, it calls DynamicExpand to produce a sub-graph and then
// walks that sub-graph independently (matching Terraform core's pattern).
// The parent graph is never mutated during the walk.
func walkGraph(graph *terraform.Graph, ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	if graph == nil {
		return nil
	}

	diags := graph.AcyclicGraph.Walk(func(vertex dag.Vertex) tfdiags.Diagnostics {
		if dag.VertexName(vertex) == "root" {
			return nil
		}

		if expandable, ok := vertex.(GraphNodeDynamicExpandable); ok {
			if shouldSkipVertex(graph, ctx, vertex) {
				markVertexSkipped(ctx, vertex, graph)
				return nil
			}
			expanded, expandDiags := expandable.DynamicExpand(ctx)
			if expandDiags.HasErrors() {
				return expandDiags
			}
			if expanded != nil {
				// Walk the sub-graph independently — the parent graph remains
				// unchanged. Results flow back via EvalContext.
				return walkSubGraph(expanded, ctx, op)
			}
			return nil
		}

		if executable, ok := vertex.(GraphNodeExecutable); ok {
			if shouldSkipVertex(graph, ctx, vertex) {
				markVertexSkipped(ctx, vertex, graph)
				return nil
			}
			return executable.Execute(ctx, op)
		}

		return nil
	})

	if op == walkOperationPlan {
		for _, step := range ctx.StepsInOrder() {
			if step.Status == runbookruntime.StepStatusPlanned {
				ctx.setStepStatusWithKey(step.Name, step.InstanceKey, runbookruntime.StepStatusCompleted, "")
			}
		}
	}

	return diags
}

// walkSubGraph walks an expanded sub-graph produced by DynamicExpand.
// Sub-graphs are flat (no nested expansion) — they contain only executable nodes.
func walkSubGraph(graph *terraform.Graph, ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	if graph == nil {
		return nil
	}

	return graph.AcyclicGraph.Walk(func(vertex dag.Vertex) tfdiags.Diagnostics {
		if dag.VertexName(vertex) == "root" {
			return nil
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
	})
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
	if step, ok := stepForVertex(ctx, vertex); ok {
		if step.Status == runbookruntime.StepStatusSkipped || step.Status == runbookruntime.StepStatusFailed {
			return
		}
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
	case *NodeStepFinalize:
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
	case *NodeStepFinalize:
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
	case *NodeStepFinalize:
		return node.Step.StepName, node.Step != nil
	default:
		return "", false
	}
}
