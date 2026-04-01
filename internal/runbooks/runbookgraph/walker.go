// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"sync"

	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type Walker[G Graph, D any] struct {
	Data      D
	Graph     G
	RootGraph G
	Operation WalkOperation

	mu *sync.Mutex
}

func NewWalker[G Graph, D any](data D, graph G, operation WalkOperation) *Walker[G, D] {
	return &Walker[G, D]{
		Data:      data,
		Graph:     graph,
		RootGraph: graph,
		Operation: operation,
		mu:        &sync.Mutex{},
	}
}

func (w *Walker[G, D]) Child(graph G) *Walker[G, D] {
	if w == nil {
		return nil
	}
	return &Walker[G, D]{
		Data:      w.Data,
		Graph:     graph,
		RootGraph: w.RootGraph,
		Operation: w.Operation,
		mu:        w.mu,
	}
}

func (w *Walker[G, D]) execute(node GraphNodeExecutable[G, D]) tfdiags.Diagnostics {
	if w == nil || node == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return node.ExecuteGraphNode(w)
}

func (w *Walker[G, D]) expand(node GraphNodeDynamicExpandable[G, D]) (G, tfdiags.Diagnostics) {
	var zero G
	if w == nil || node == nil {
		return zero, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return node.DynamicExpand(w)
}

func Walk[G Graph, D any](graph G, walker *Walker[G, D]) tfdiags.Diagnostics {
	if walker == nil || graph.DAG() == nil {
		return nil
	}

	walkFn := func(v dag.Vertex) tfdiags.Diagnostics {
		var diags tfdiags.Diagnostics

		if executable, ok := v.(GraphNodeExecutable[G, D]); ok {
			diags = diags.Append(walker.execute(executable))
			if diags.HasErrors() {
				return diags
			}
		}

		if expandable, ok := v.(GraphNodeDynamicExpandable[G, D]); ok {
			subgraph, moreDiags := walker.expand(expandable)
			diags = diags.Append(moreDiags)
			if diags.HasErrors() {
				return diags
			}
			if subgraph.DAG() != nil {
				if len(subgraph.DAG().Cycles()) != 0 {
					return diags.Append(tfdiags.Sourceless(
						tfdiags.Error,
						"Invalid runbook graph",
						"Cannot walk a runbook subgraph while it contains cycles.",
					))
				}
				diags = diags.Append(Walk(subgraph, walker.Child(subgraph)))
			}
		}

		return diags
	}

	return graph.DAG().Walk(walkFn)
}
