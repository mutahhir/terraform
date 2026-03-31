// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/collections"
)

// WorkspaceOutputValue is the address of an output value in the Terraform
// workspace that a runbook expression can reference.
type WorkspaceOutputValue struct {
	Output addrs.AbsOutputValue
}

func (WorkspaceOutputValue) referenceableSigil() {}

func (o WorkspaceOutputValue) String() string {
	return "workspace." + o.Output.String()
}

func (o WorkspaceOutputValue) UniqueKey() collections.UniqueKey[WorkspaceOutputValue] {
	return workspaceOutputValueKey(o.String())
}

type workspaceOutputValueKey string

// IsUniqueKey implements collections.UniqueKey.
func (workspaceOutputValueKey) IsUniqueKey(WorkspaceOutputValue) {}
