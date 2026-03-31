// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/collections"
)

// WorkspaceActionInstance is the address of an action in the Terraform
// workspace that a runbook expression can reference, optionally including a
// specific instance key.
type WorkspaceActionInstance struct {
	Action addrs.AbsAction
	Key    addrs.InstanceKey
}

func (WorkspaceActionInstance) executableActionSigil() {}
func (WorkspaceActionInstance) referenceableSigil()    {}

func (a WorkspaceActionInstance) String() string {
	if a.Key == nil {
		return "workspace." + a.Action.String()
	}
	return "workspace." + addrs.AbsActionInstance{Action: addrs.ActionInstance{Action: a.Action.Action, Key: a.Key}, Module: a.Action.Module}.String()
}

func (a WorkspaceActionInstance) UniqueKey() collections.UniqueKey[WorkspaceActionInstance] {
	return workspaceActionKey(a.String())
}

type workspaceActionKey string

// IsUniqueKey implements collections.UniqueKey.
func (workspaceActionKey) IsUniqueKey(WorkspaceActionInstance) {}
