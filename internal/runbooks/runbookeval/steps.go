// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookeval

import (
	"sort"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/zclconf/go-cty/cty"
)

type StepResults struct {
	groups map[string]*stepGroup
}

type stepGroup struct {
	singleton    cty.Value
	hasSingleton bool
	count        map[int]cty.Value
	forEach      map[string]cty.Value
}

func NewStepResults() *StepResults {
	return &StepResults{groups: map[string]*stepGroup{}}
}

func (r *StepResults) Set(addr runbookaddrs.StepInstance, value cty.Value) {
	if r == nil {
		return
	}
	group := r.group(addr.Step.Name)
	switch key := addr.Key.(type) {
	case nil:
		group.singleton = value
		group.hasSingleton = true
	case addrs.IntKey:
		if group.count == nil {
			group.count = map[int]cty.Value{}
		}
		group.count[int(key)] = value
	case addrs.StringKey:
		if group.forEach == nil {
			group.forEach = map[string]cty.Value{}
		}
		group.forEach[string(key)] = value
	}
}

func (r *StepResults) Get(addr runbookaddrs.StepInstance) cty.Value {
	if r == nil {
		return cty.NilVal
	}
	group := r.groups[addr.Step.Name]
	if group == nil {
		return cty.NilVal
	}
	switch key := addr.Key.(type) {
	case nil:
		if group.hasSingleton {
			return group.singleton
		}
	case addrs.IntKey:
		if group.count != nil {
			return group.count[int(key)]
		}
	case addrs.StringKey:
		if group.forEach != nil {
			return group.forEach[string(key)]
		}
	}
	return cty.NilVal
}

func (r *StepResults) ScopeValue() cty.Value {
	if r == nil || len(r.groups) == 0 {
		return cty.EmptyObjectVal
	}
	ret := make(map[string]cty.Value, len(r.groups))
	for name, group := range r.groups {
		ret[name] = group.scopeValue()
	}
	return cty.ObjectVal(ret)
}

func (r *StepResults) group(name string) *stepGroup {
	if group, ok := r.groups[name]; ok {
		return group
	}
	group := &stepGroup{}
	r.groups[name] = group
	return group
}

func (g *stepGroup) scopeValue() cty.Value {
	if g == nil {
		return cty.EmptyObjectVal
	}
	if g.hasSingleton {
		return g.singleton
	}
	if len(g.count) != 0 {
		keys := make([]int, 0, len(g.count))
		for k := range g.count {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		vals := make([]cty.Value, 0, len(keys))
		for _, k := range keys {
			vals = append(vals, g.count[k])
		}
		return cty.TupleVal(vals)
	}
	if len(g.forEach) != 0 {
		vals := make(map[string]cty.Value, len(g.forEach))
		for k, v := range g.forEach {
			vals[k] = v
		}
		return cty.ObjectVal(vals)
	}
	return cty.EmptyObjectVal
}
