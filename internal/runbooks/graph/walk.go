package runbookgraph

import (
	"fmt"

	"github.com/hashicorp/terraform/internal/dag"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// defaultRunbookParallelism bounds how many step/operation callbacks run
// concurrently during a graph walk, mirroring Terraform core's default
// -parallelism of 10. The runbook DAG walker (internal/dag) otherwise spawns a
// goroutine per vertex with no cap (hc-terraform-wdc.1).
const defaultRunbookParallelism = 10

// walkSemaphore bounds the number of concurrently executing graph-walk
// callbacks. A zero-value walkSemaphore (limit <= 0) is unbounded, preserving
// the historical behavior for callers that opt out.
type walkSemaphore struct {
	sem terraform.Semaphore
}

func newWalkSemaphore(parallelism int) walkSemaphore {
	if parallelism <= 0 {
		return walkSemaphore{}
	}
	return walkSemaphore{sem: terraform.NewSemaphore(parallelism)}
}

func (w walkSemaphore) Acquire() {
	if w.sem != nil {
		w.sem.Acquire()
	}
}

func (w walkSemaphore) Release() {
	if w.sem != nil {
		w.sem.Release()
	}
}

type walkOperation string

const (
	walkOperationPlan    walkOperation = "plan"
	walkOperationExecute walkOperation = "execute"
)

type GraphNodeExecutable interface {
	Execute(EvalContext, walkOperation) tfdiags.Diagnostics
}

type GraphNodeDynamicExpandable interface {
	DynamicExpand(EvalContext) (*terraform.Graph, tfdiags.Diagnostics)
}

// walkGraph walks the given graph, executing nodes according to the operation.
// For expandable nodes, it calls DynamicExpand to produce a sub-graph and then
// walks that sub-graph independently (matching Terraform core's pattern).
// The parent graph is never mutated during the walk.
//
// parallelism bounds how many executable callbacks run concurrently across this
// graph and any sub-graphs it expands; values <= 0 mean unbounded. The cap is
// applied only around leaf executable nodes (where provider work happens) and
// never around the expandable branch, so an expanding vertex never holds a slot
// while its sub-graph contends for slots.
func walkGraph(graph *terraform.Graph, ctx EvalContext, op walkOperation, parallelism int) tfdiags.Diagnostics {
	if graph == nil {
		return nil
	}

	sem := newWalkSemaphore(parallelism)

	diags := graph.AcyclicGraph.Walk(func(vertex dag.Vertex) tfdiags.Diagnostics {
		if dag.VertexName(vertex) == "root" {
			return nil
		}

		// Check for cancellation before processing each vertex
		if err := ctx.StopCtx().Err(); err != nil {
			return tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
				tfdiags.Error,
				"Runbook execution cancelled",
				"The runbook execution was cancelled.",
			))
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
				// unchanged. Results flow back via EvalContext. The same
				// semaphore is shared so the overall concurrency cap spans the
				// parent and its sub-graphs.
				return walkSubGraph(expanded, ctx, op, sem)
			}
			return nil
		}

		if executable, ok := vertex.(GraphNodeExecutable); ok {
			if shouldSkipVertex(graph, ctx, vertex) {
				markVertexSkipped(ctx, vertex, graph)
				return nil
			}
			execDiags := func() tfdiags.Diagnostics {
				sem.Acquire()
				defer sem.Release()
				return executable.Execute(ctx, op)
			}()
			if execDiags.HasErrors() && op == walkOperationExecute {
				if step, ok := stepForVertex(ctx, vertex); ok && step != nil {
					executeCatchBlocks(ctx, step, execDiags)
				}
			}
			return execDiags
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
// It shares the parent walk's semaphore so the concurrency cap spans the whole
// walk rather than resetting per sub-graph.
func walkSubGraph(graph *terraform.Graph, ctx EvalContext, op walkOperation, sem walkSemaphore) tfdiags.Diagnostics {
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

		execDiags := func() tfdiags.Diagnostics {
			sem.Acquire()
			defer sem.Release()
			return executable.Execute(ctx, op)
		}()
		if execDiags.HasErrors() && op == walkOperationExecute {
			if step, ok := stepForVertex(ctx, vertex); ok && step != nil {
				executeCatchBlocks(ctx, step, execDiags)
			}
		}
		return execDiags
	})
}

func shouldSkipVertex(graph *terraform.Graph, ctx EvalContext, vertex dag.Vertex) bool {
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

func markVertexSkipped(ctx EvalContext, vertex dag.Vertex, graph *terraform.Graph) {
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

func stepForVertex(ctx EvalContext, vertex dag.Vertex) (*runbookruntime.Step, bool) {
	if belonging, ok := vertex.(StepBelonging); ok {
		step := belonging.OwningStep()
		if step == nil {
			return nil, false
		}
		return ctx.stepWithKey(step.StepName, step.InstanceKey)
	}
	stepName, ok := stepNameForVertex(vertex)
	if !ok {
		return nil, false
	}
	return ctx.Step(stepName)
}

func setStepStatusForVertex(ctx EvalContext, vertex dag.Vertex, status runbookruntime.StepStatus, reason string) {
	if belonging, ok := vertex.(StepBelonging); ok {
		step := belonging.OwningStep()
		if step != nil {
			ctx.setStepStatusWithKey(step.StepName, step.InstanceKey, status, reason)
		}
		return
	}
	if stepName, ok := stepNameForVertex(vertex); ok {
		ctx.SetStepStatus(stepName, status, reason)
	}
}

func hasDependencyStateForVertex(ctx EvalContext, vertex dag.Vertex, statuses ...runbookruntime.StepStatus) bool {
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

// StepBelonging is implemented by graph nodes that belong to a specific step
// instance. It replaces exhaustive type switches for vertex resolution.
type StepBelonging interface {
	OwningStep() *NodeStepInstance
}

func stepNameForVertex(vertex dag.Vertex) (string, bool) {
	if belonging, ok := vertex.(StepBelonging); ok {
		step := belonging.OwningStep()
		if step != nil {
			return step.StepName, true
		}
		return "", false
	}
	if expand, ok := vertex.(*NodeExpandStep); ok {
		return expand.StepName, true
	}
	return "", false
}
