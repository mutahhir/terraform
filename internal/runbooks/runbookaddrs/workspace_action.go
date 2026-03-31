// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/collections"
)

// WorkspaceAction is the address of an action in the Terraform workspace that a
// runbook expression can reference.
type WorkspaceAction struct {
	Action addrs.AbsAction
}

func (WorkspaceAction) executableActionSigil() {}
func (WorkspaceAction) referenceableSigil()    {}

func (a WorkspaceAction) String() string {
	return "workspace." + a.Action.String()
}

func (a WorkspaceAction) UniqueKey() collections.UniqueKey[WorkspaceAction] {
	return workspaceActionKey(a.String())
}

type workspaceActionKey string

// IsUniqueKey implements collections.UniqueKey.
func (workspaceActionKey) IsUniqueKey(WorkspaceAction) {}

// WorkspaceActionInstance is the address of a concrete action instance in the
// Terraform workspace that a runbook expression can reference.
type WorkspaceActionInstance struct {
	Action addrs.AbsActionInstance
}

func (WorkspaceActionInstance) executableActionSigil() {}
func (WorkspaceActionInstance) referenceableSigil()    {}

func (a WorkspaceActionInstance) String() string {
	return "workspace." + a.Action.String()
}

func (a WorkspaceActionInstance) UniqueKey() collections.UniqueKey[WorkspaceActionInstance] {
	return workspaceActionInstanceKey(a.String())
}

type workspaceActionInstanceKey string

// IsUniqueKey implements collections.UniqueKey.
func (workspaceActionInstanceKey) IsUniqueKey(WorkspaceActionInstance) {}
