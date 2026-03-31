// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/instances"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

type Step struct {
	context          *RunbookContext
	config           *runbookconfig.Step
	unknownInstances map[addrs.InstanceKey]*StepInstance
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
	insts, unknown, _ := s.CheckInstances()
	return insts, !unknown
}

func (s *Step) CheckInstances() (map[addrs.InstanceKey]*StepInstance, bool, tfdiags.Diagnostics) {
	if s == nil || s.config == nil {
		return nil, false, nil
	}
	eval := newRepetitionEvaluator(s.context)
	return eval.stepInstances(s)
}

func (s *Step) UnknownInstance(key addrs.InstanceKey) *StepInstance {
	if s == nil {
		return nil
	}
	if s.unknownInstances == nil {
		s.unknownInstances = make(map[addrs.InstanceKey]*StepInstance)
	}
	if inst, ok := s.unknownInstances[key]; ok {
		return inst
	}

	repetition := instances.TotallyUnknownRepetitionData
	if s.config != nil {
		switch {
		case s.config.Count != nil:
			repetition = instances.UnknownCountRepetitionData
		case s.config.ForEach != nil:
			repetition = unknownForEachRepetitionData(s)
		}
	}
	if key != addrs.WildcardKey && repetition.EachKey != cty.NilVal {
		repetition.EachKey = key.Value()
	}

	inst := &StepInstance{
		step:       s,
		addr:       runbookaddrs.StepInstance{Step: s.Addr(), Key: key},
		repetition: repetition,
	}
	s.unknownInstances[key] = inst
	return inst
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
