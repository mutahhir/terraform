// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import "github.com/hashicorp/terraform/internal/tfdiags"

type GraphBuilder[G any] interface {
	Build() (G, tfdiags.Diagnostics)
}

type GraphTransformer[G any] interface {
	Transform(G) error
}

type BasicGraphBuilder[G any] struct {
	Steps []GraphTransformer[G]
	Name  string
	Graph G
}

func (b *BasicGraphBuilder[G]) Build() (G, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	g := b.Graph
	for _, step := range b.Steps {
		if step == nil {
			continue
		}
		if err := step.Transform(g); err != nil {
			if diag, ok := err.(tfdiags.DiagnosticsAsError); ok {
				diags = diags.Append(diag.Diagnostics)
				return g, diags
			}
			diags = diags.Append(err)
			return g, diags
		}
	}
	return g, diags
}
