// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import "github.com/hashicorp/terraform/internal/dag"

type OutputTransformer struct {
	Context *RunbookContext
}

func (t *OutputTransformer) Transform(g *PlanGraph) error {
	if t.Context == nil || g == nil || g.Graph == nil {
		return nil
	}
	for name := range t.Context.config.Outputs {
		vertex := runbookOutputVertex{NameValue: name}
		g.Graph.Add(vertex)
		g.Graph.Connect(dag.BasicEdge(g.Root, vertex))
	}
	return nil
}
