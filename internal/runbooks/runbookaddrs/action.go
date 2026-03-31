// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"github.com/hashicorp/terraform/internal/addrs"
)

// ExecutableAction is an action address that can appear in an execute block.
//
// This includes both actions declared inside the current step and actions in
// the referenced Terraform workspace.
type ExecutableAction interface {
	executableActionSigil()
	String() string
}

// Action is the relative address of an action declared inside the current
// step.
type Action struct {
	Type string
	Name string
}

func (Action) executableActionSigil() {}

func (a Action) String() string {
	return "action." + a.Type + "." + a.Name
}

// ActionInvocationInstance is the relative address of a concrete action
// instance declared inside the current step.
type ActionInvocationInstance struct {
	Action Action
	Key    addrs.InstanceKey
}

func (ActionInvocationInstance) executableActionSigil() {}

func (a ActionInvocationInstance) String() string {
	if a.Key == nil {
		return a.Action.String()
	}
	return a.Action.String() + a.Key.String()
}
