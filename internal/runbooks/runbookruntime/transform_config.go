// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

type ConfigTransformer struct {
	Context *RunbookContext
}

func (t *ConfigTransformer) Transform(g *PlanGraph) error {
	if g == nil || g.Graph == nil {
		return nil
	}
	if t.Context != nil {
		t.Context.resetGraphBuildState()
	}
	g.Root = runbookRootVertex{}
	g.Graph.Add(g.Root)
	return nil
}
