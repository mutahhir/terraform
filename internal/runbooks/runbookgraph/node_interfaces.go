// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type GraphNodeExecutable interface {
	Execute(*RunbookContext)
}

type GraphNodeDynamicExpandable interface {
	DynamicExpand(*RunbookContext) (*Graph, tfdiags.Diagnostics)
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
	Referenceable() runbookaddrs.Referenceable
}

type GraphNodeReferencer interface {
	References() []*runbookaddrs.Reference
}

type GraphNodeReferenceOutside interface {
	ReferenceOutside() (selfScope, referenceScope Scope)
}
