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

func walkGraph(graph *terraform.Graph, ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	if graph == nil {
		return nil
	}

	// TODO: Switch this to the concurrent dag walker once EvalContext and node
	// execution are safe for real parallelism. The current walk mutates shared
	// runbook state during execution and relies on deterministic sequential order.
	order := graph.TopologicalOrder()
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}

	var diags tfdiags.Diagnostics
	for _, vertex := range order {
		if dag.VertexName(vertex) == "root" {
			continue
		}
		if expandable, ok := vertex.(GraphNodeDynamicExpandable); ok {
			if shouldSkipVertex(graph, ctx, vertex) {
				markVertexSkipped(ctx, vertex, graph)
				continue
			}
			expanded, expandDiags := expandable.DynamicExpand(ctx)
			diags = diags.Append(expandDiags)
			if expandDiags.HasErrors() {
				continue
			}
			diags = diags.Append(walkGraph(expanded, ctx, op))
			continue
		}
		executable, ok := vertex.(GraphNodeExecutable)
		if !ok {
			continue
		}
		if shouldSkipVertex(graph, ctx, vertex) {
			markVertexSkipped(ctx, vertex, graph)
			continue
		}
		diags = diags.Append(executable.Execute(ctx, op))
	}

	return diags
}

func shouldSkipVertex(graph *terraform.Graph, ctx *EvalContext, vertex dag.Vertex) bool {
	stepName, ok := stepNameForVertex(vertex)
	if !ok {
		return false
	}
	if ctx.HasDependencyState(stepName, runbookruntime.StepStatusSkipped, runbookruntime.StepStatusFailed) {
		return true
	}
	for _, dep := range graph.DownEdges(vertex) {
		depStep, ok := stepNameForVertex(dep)
		if !ok || depStep == stepName {
			continue
		}
		if ctx.HasDependencyState(depStep, runbookruntime.StepStatusSkipped, runbookruntime.StepStatusFailed) {
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
			ctx.SetStepStatus(stepName, runbookruntime.StepStatusSkipped, fmt.Sprintf("dependency step %q did not complete", depStep))
			return
		}
	}
	ctx.SetStepStatus(stepName, runbookruntime.StepStatusSkipped, "step did not execute")
}

func stepNameForVertex(vertex dag.Vertex) (string, bool) {
	switch node := vertex.(type) {
	case *NodeExpandStep:
		return node.StepName, true
	case *NodeStepInstance:
		return node.StepName, true
	case *NodeStepAction:
		return node.StepName, true
	case *NodeStepData:
		return node.StepName, true
	case *NodeStepList:
		return node.StepName, true
	case *NodeStepLocal:
		return node.StepName, true
	case *NodeStepExecution:
		return node.StepName, true
	case *NodeStepCondition:
		return node.StepName, true
	case *NodeStepOutput:
		return node.StepName, true
	default:
		return "", false
	}
}
