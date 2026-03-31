// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/instances"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/gocty"
)

type Step struct {
	context *RunbookContext
	config  *runbookconfig.Step
}

func (s *Step) Config() *runbookconfig.Step {
	if s == nil {
		return nil
	}
	return s.config
}

func (s *Step) Name() string {
	if s == nil || s.config == nil {
		return ""
	}
	return s.config.Name
}

func (s *Step) Addr() runbookaddrs.ConfigStep {
	return runbookaddrs.ConfigStep{Name: s.Name()}
}

func (s *Step) Instances() (map[addrs.InstanceKey]*StepInstance, bool) {
	if s == nil || s.config == nil {
		return nil, false
	}

	baseAddr := runbookaddrs.StepInstance{Step: s.Addr()}
	if s.config.Count == nil && s.config.ForEach == nil {
		return map[addrs.InstanceKey]*StepInstance{
			addrs.NoKey: {
				step: s,
				addr: baseAddr,
			},
		}, true
	}

	if s.config.Count != nil {
		count, ok := staticCountValue(s.config.Count)
		if !ok {
			return nil, false
		}
		ret := make(map[addrs.InstanceKey]*StepInstance, count)
		for i := range count {
			key := addrs.IntKey(i)
			ret[key] = &StepInstance{
				step: s,
				addr: runbookaddrs.StepInstance{
					Step: baseAddr.Step,
					Key:  key,
				},
				repetition: instances.RepetitionData{
					CountIndex: cty.NumberIntVal(int64(i)),
				},
			}
		}
		return ret, true
	}

	mapping, ok := staticForEachValue(s.config.ForEach)
	if !ok {
		return nil, false
	}

	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	ret := make(map[addrs.InstanceKey]*StepInstance, len(keys))
	for _, keyStr := range keys {
		key := addrs.StringKey(keyStr)
		ret[key] = &StepInstance{
			step: s,
			addr: runbookaddrs.StepInstance{
				Step: baseAddr.Step,
				Key:  key,
			},
			repetition: instances.RepetitionData{
				EachKey:   cty.StringVal(keyStr),
				EachValue: mapping[keyStr],
			},
		}
	}
	return ret, true
}

type StepInstance struct {
	step       *Step
	addr       runbookaddrs.StepInstance
	repetition instances.RepetitionData
}

func (s *StepInstance) Step() *Step {
	if s == nil {
		return nil
	}
	return s.step
}

func (s *StepInstance) Addr() runbookaddrs.StepInstance {
	if s == nil {
		return runbookaddrs.StepInstance{}
	}
	return s.addr
}

func (s *StepInstance) RepetitionData() instances.RepetitionData {
	if s == nil {
		return instances.RepetitionData{}
	}
	return s.repetition
}

func staticCountValue(expr hcl.Expression) (int, bool) {
	if expr == nil {
		return 0, false
	}
	val, diags := expr.Value(nil)
	if diags.HasErrors() || !val.IsKnown() || val.IsNull() || val.Type() != cty.Number {
		return 0, false
	}
	var ret int
	if err := gocty.FromCtyValue(val, &ret); err != nil || ret < 0 {
		return 0, false
	}
	return ret, true
}

func staticForEachValue(expr hcl.Expression) (map[string]cty.Value, bool) {
	if expr == nil {
		return nil, false
	}
	val, diags := expr.Value(nil)
	if diags.HasErrors() || !val.IsKnown() || val.IsNull() {
		return nil, false
	}

	ty := val.Type()
	switch {
	case ty.IsMapType() || ty.IsObjectType():
		return val.AsValueMap(), true
	case ty.IsSetType() && ty.ElementType() == cty.String:
		elems := val.AsValueSlice()
		ret := make(map[string]cty.Value, len(elems))
		for _, elem := range elems {
			ret[elem.AsString()] = elem
		}
		return ret, true
	default:
		return nil, false
	}
}
