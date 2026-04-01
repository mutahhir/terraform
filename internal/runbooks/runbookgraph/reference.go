// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"fmt"

	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
)

type ReferenceMap map[string][]dag.Vertex

func NewReferenceMap(vs []dag.Vertex) ReferenceMap {
	m := make(ReferenceMap)
	for _, v := range vs {
		rn, ok := v.(GraphNodeReferenceable)
		if !ok {
			continue
		}
		scope := vertexReferenceableScope(v)
		for _, addr := range rn.ReferenceableAddrs() {
			key := m.mapKey(scope, addr)
			m[key] = append(m[key], v)
		}
	}
	return m
}

func (m ReferenceMap) References(v dag.Vertex) []dag.Vertex {
	rn, ok := v.(GraphNodeReferencer)
	if !ok {
		return nil
	}
	var matches []dag.Vertex
	for _, ref := range rn.References() {
		scope := ref.Scope
		if scope == nil {
			scope = vertexReferenceScope(v)
		}
		key := m.mapKey(scope, ref.Target)
		for _, rv := range m[key] {
			if rv == v {
				continue
			}
			matches = append(matches, rv)
		}
	}
	return matches
}

func (m ReferenceMap) mapKey(scope Scope, addr ReferenceTarget) string {
	addr = normalizeReferenceTarget(addr)
	return fmt.Sprintf("%s|%s", scope.String(), addr.String())
}

func normalizeReferenceTarget(addr ReferenceTarget) ReferenceTarget {
	switch target := addr.(type) {
	case runbookaddrs.StepOutputValue:
		return target.ConfigStepOutputValue()
	default:
		return addr
	}
}

func vertexReferenceableScope(v dag.Vertex) Scope {
	scoped := v.(GraphNodeScope)
	if outside, ok := v.(GraphNodeReferenceOutside); ok {
		selfScope, _ := outside.ReferenceOutside()
		return selfScope
	}
	return scoped.Scope()
}

func vertexReferenceScope(v dag.Vertex) Scope {
	scoped := v.(GraphNodeScope)
	if outside, ok := v.(GraphNodeReferenceOutside); ok {
		_, refScope := outside.ReferenceOutside()
		return refScope
	}
	return scoped.Scope()
}
