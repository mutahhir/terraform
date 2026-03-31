// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import "github.com/hashicorp/terraform/internal/collections"

// Workspace is the address of the Terraform workspace namespace that runbook
// expressions can reference.
type Workspace struct{}

func (Workspace) String() string {
	return "workspace"
}

func (Workspace) UniqueKey() collections.UniqueKey[Workspace] {
	return Workspace{}
}

// A Workspace is its own [collections.UniqueKey].
func (Workspace) IsUniqueKey(Workspace) {}
