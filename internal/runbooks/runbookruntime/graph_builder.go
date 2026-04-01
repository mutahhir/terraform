// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type GraphBuilder interface {
	Build() (*PlanGraph, tfdiags.Diagnostics)
}

type GraphTransformer interface {
	Transform(*PlanGraph) error
}

type BasicGraphBuilder struct {
	Steps []GraphTransformer
	Name  string
	Graph *PlanGraph
}

func (b *BasicGraphBuilder) Build() (*PlanGraph, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	g := b.Graph
	if g == nil {
		g = &PlanGraph{
			Graph:           &dag.AcyclicGraph{},
			ConfigSteps:     map[string]runbookStepVertex{},
			StepVertices:    map[string]planStepVertex{},
			InstancesByStep: map[string]map[addrs.InstanceKey]*StepInstance{},
		}
	}
	for _, step := range b.Steps {
		if step == nil {
			continue
		}
		if err := step.Transform(g); err != nil {
			if diag, ok := err.(tfdiags.DiagnosticsAsError); ok {
				diags = diags.Append(diag.Diagnostics)
				return nil, diags
			}
			diags = diags.Append(err)
			return nil, diags
		}
	}
	return g, diags
}
