// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"sort"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type walkOperation byte

const (
	walkInvalid walkOperation = iota
	walkValidate
	walkPlan
	walkExecute
)

type PlanGraph struct {
	Config *runbookconfig.RunbookConfig

	Operation walkOperation

	Graph           *dag.AcyclicGraph
	Root            *nodeRunbookRoot
	ConfigSteps     map[string]*nodeExpandRunbookStep
	StepVertices    map[string]*nodeExpandRunbookStepInstance
	InstancesByStep map[string]map[addrs.InstanceKey]*StepInstance
}

func (g *PlanGraph) Walk(walker *RunbookGraphWalker) tfdiags.Diagnostics {
	if g == nil || g.Graph == nil || walker == nil {
		return nil
	}

	walkFn := func(v dag.Vertex) tfdiags.Diagnostics {
		var diags tfdiags.Diagnostics

		if executable, ok := v.(runbookGraphNodeExecutable); ok {
			diags = diags.Append(walker.execute(executable))
			if diags.HasErrors() {
				return diags
			}
		}

		if expandable, ok := v.(runbookGraphNodeDynamicExpandable); ok {
			subgraph, moreDiags := walker.expand(expandable)
			diags = diags.Append(moreDiags)
			if diags.HasErrors() {
				return diags
			}
			if subgraph != nil {
				if len(subgraph.Graph.Cycles()) != 0 {
					return diags.Append(tfdiags.Sourceless(
						tfdiags.Error,
						"Invalid runbook graph",
						"Cannot walk a runbook subgraph while it contains cycles.",
					))
				}
				diags = diags.Append(subgraph.Walk(walker.child(subgraph)))
			}
		}

		return diags
	}

	return g.Graph.Walk(walkFn)
}

type RunbookPlanGraphBuilder struct {
	Context   *RunbookContext
	Opts      *PlanOpts
	Operation walkOperation
}

func (b *RunbookPlanGraphBuilder) Build() (*PlanGraph, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if b == nil || b.Context == nil {
		return nil, diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Invalid plan graph builder",
			"A runbook plan graph builder requires a non-nil runtime context.",
		))
	}

	builder := &BasicGraphBuilder{
		Name: "RunbookPlanGraphBuilder",
		Graph: &PlanGraph{
			Config:          b.Context.config,
			Operation:       b.Operation,
			Graph:           &dag.AcyclicGraph{},
			ConfigSteps:     map[string]*nodeExpandRunbookStep{},
			StepVertices:    map[string]*nodeExpandRunbookStepInstance{},
			InstancesByStep: map[string]map[addrs.InstanceKey]*StepInstance{},
		},
		Steps: []GraphTransformer{
			&ConfigTransformer{Context: b.Context},
			&RootVariableTransformer{Context: b.Context},
			&OutputTransformer{Context: b.Context},
			&StepTransformer{Context: b.Context, Operation: b.Operation},
			&runbookTransitiveReductionTransformer{},
		},
	}
	graph, moreDiags := builder.Build()
	diags = diags.Append(moreDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	if len(graph.Graph.Cycles()) != 0 {
		for _, cycle := range graph.Graph.Cycles() {
			cycleNames := make([]string, 0, len(cycle))
			for _, raw := range cycle {
				switch v := raw.(type) {
				case *nodeExpandRunbookStep:
					cycleNames = append(cycleNames, v.Name())
				}
			}
			sort.Strings(cycleNames)
			summary := "Invalid runbook graph"
			detail := "Cannot build a runbook graph while the dependency graph contains cycles."
			if len(cycleNames) != 0 {
				summary = "Cycle: " + cycleNames[0]
				if len(cycleNames) > 1 {
					summary = "Cycle: " + cycleNames[0]
					for _, name := range cycleNames[1:] {
						summary += ", " + name
					}
				}
				detail = ""
			}
			diags = diags.Append(tfdiags.Sourceless(tfdiags.Error, summary, detail))
		}
		return nil, diags
	}

	return graph, diags
}
