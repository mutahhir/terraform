// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"fmt"

	"github.com/hashicorp/terraform/internal/addrs"
)

// Step is the address of a runbook step block, which may have multiple
// instances if it uses count or for_each.
type Step struct {
	Name string
}

func (s Step) String() string {
	return s.Name
}

func (s Step) Instance(key addrs.InstanceKey) StepInstance {
	return StepInstance{Step: s, Key: key}
}

// StepInstance is the address of a concrete runbook step instance.
type StepInstance struct {
	Step Step
	Key  addrs.InstanceKey
}

func (s StepInstance) String() string {
	if s.Key == addrs.NoKey {
		return s.Step.String()
	}
	return fmt.Sprintf("%s%s", s.Step.String(), s.Key.String())
}
