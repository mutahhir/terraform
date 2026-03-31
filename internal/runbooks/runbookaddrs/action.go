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

// ActionInstance is the relative address of an action declared inside the
// current step, optionally including a specific instance key.
type ActionInstance struct {
	Type string
	Name string
	Key  addrs.InstanceKey
}

func (ActionInstance) executableActionSigil() {}

func (a ActionInstance) String() string {
	base := "action." + a.Type + "." + a.Name
	if a.Key == nil {
		return base
	}
	return base + a.Key.String()
}
