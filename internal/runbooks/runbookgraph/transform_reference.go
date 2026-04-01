// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import "github.com/hashicorp/terraform/internal/dag"

type ReferenceTransformer[G Graph] struct{}

func (t *ReferenceTransformer[G]) Transform(g G) error {
	if g.DAG() == nil {
		return nil
	}
	refs := NewReferenceMap(g.DAG().Vertices())
	for _, v := range g.DAG().Vertices() {
		for _, parent := range refs.References(v) {
			g.DAG().Connect(dag.BasicEdge(v, parent))
		}
	}
	return nil
}
