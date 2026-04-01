// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import terraform "github.com/hashicorp/terraform/internal/terraform"

// runbookTransitiveReductionTransformer reuses Terraform's existing graph
// transformer for the generic transitive-reduction pass.
type runbookTransitiveReductionTransformer struct{}

func (t *runbookTransitiveReductionTransformer) Transform(g *PlanGraph) error {
	if g == nil || g.Graph == nil {
		return nil
	}

	adapted := &terraform.Graph{}
	adapted.AcyclicGraph = *g.Graph

	if err := (&terraform.TransitiveReductionTransformer{}).Transform(adapted); err != nil {
		return err
	}

	g.Graph = &adapted.AcyclicGraph
	return nil
}
