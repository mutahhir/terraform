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
	for name, output := range t.Context.config.Outputs {
		node := &nodeRunbookOutput{NameValue: name, Output: output}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(g.Root, node))
	}
	return nil
}
