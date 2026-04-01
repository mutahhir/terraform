// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type GraphNodeExecutable[G Graph, D any] interface {
	dag.Vertex
	ExecuteGraphNode(*Walker[G, D]) tfdiags.Diagnostics
}

type GraphNodeDynamicExpandable[G Graph, D any] interface {
	dag.Vertex
	DynamicExpand(*Walker[G, D]) (G, tfdiags.Diagnostics)
}

type GraphNodeScope interface {
	Scope() Scope
}

type ReferenceTarget interface {
	String() string
}

type Reference struct {
	Target      ReferenceTarget
	Scope       Scope
	SourceRange tfdiags.SourceRange
}

type GraphNodeReferenceable interface {
	GraphNodeScope
	ReferenceableAddrs() []ReferenceTarget
}

type GraphNodeReferencer interface {
	GraphNodeScope
	References() []Reference
}

type GraphNodeReferenceOutside interface {
	ReferenceOutside() (selfScope, referenceScope Scope)
}
