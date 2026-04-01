// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"sort"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/runbooks/runbookgraph"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type PlanGraph struct {
	Config *runbookconfig.RunbookConfig

	Operation runbookgraph.WalkOperation

	Graph           *dag.AcyclicGraph
	Root            *nodeRunbookRoot
	ConfigSteps     map[string]*nodeExpandRunbookStep
	StepVertices    map[string]*nodeExpandRunbookStepInstance
	InstancesByStep map[string]map[addrs.InstanceKey]*StepInstance
}

func (g *PlanGraph) DAG() *dag.AcyclicGraph {
	if g == nil {
		return nil
	}
	return g.Graph
}

type RunbookPlanGraphBuilder struct {
	Context   *RunbookContext
	Opts      *PlanOpts
	Operation runbookgraph.WalkOperation
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

	builder := &runbookgraph.BasicGraphBuilder[*PlanGraph]{
		Name: "RunbookPlanGraphBuilder",
		Graph: &PlanGraph{
			Config:          b.Context.config,
			Operation:       b.Operation,
			Graph:           &dag.AcyclicGraph{},
			ConfigSteps:     map[string]*nodeExpandRunbookStep{},
			StepVertices:    map[string]*nodeExpandRunbookStepInstance{},
			InstancesByStep: map[string]map[addrs.InstanceKey]*StepInstance{},
		},
		Steps: []runbookgraph.GraphTransformer[*PlanGraph]{
			&ConfigTransformer{Context: b.Context},
			&RootVariableTransformer{Context: b.Context},
			&OutputTransformer{Context: b.Context},
			&StepTransformer{Context: b.Context, Operation: b.Operation},
			&runbookgraph.ReferenceTransformer[*PlanGraph]{},
			&runbookgraph.TransitiveReductionTransformer[*PlanGraph]{},
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
