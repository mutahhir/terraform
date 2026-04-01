// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import terraform "github.com/hashicorp/terraform/internal/terraform"

type TransitiveReductionTransformer[G Graph] struct{}

func (t *TransitiveReductionTransformer[G]) Transform(g G) error {
	if g.DAG() == nil {
		return nil
	}

	adapted := &terraform.Graph{}
	adapted.AcyclicGraph = *g.DAG()

	if err := (&terraform.TransitiveReductionTransformer{}).Transform(adapted); err != nil {
		return err
	}

	*g.DAG() = adapted.AcyclicGraph
	return nil
}
