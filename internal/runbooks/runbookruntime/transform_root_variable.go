// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import "github.com/hashicorp/terraform/internal/dag"

type RootVariableTransformer struct {
	Context *RunbookContext
}

func (t *RootVariableTransformer) Transform(g *PlanGraph) error {
	if t.Context == nil || g == nil || g.Graph == nil {
		return nil
	}
	for name, variable := range t.Context.config.Variables {
		node := &nodeRootVariable{NameValue: name, Variable: variable}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(g.Root, node))
	}
	return nil
}
