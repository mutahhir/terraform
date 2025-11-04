// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: BUSL-1.1

package hooks

import (
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/plans"
	"github.com/hashicorp/terraform/internal/stacks/stackaddrs"
)

type ActionInvocation struct {
	Addr         stackaddrs.AbsActionInvocationInstance
	ProviderAddr addrs.Provider
	Trigger      plans.ActionTrigger
}

type Action struct {
	ComponentInstance stackaddrs.AbsComponentInstance
	Addr              string
	ProviderAddr      addrs.Provider
	Type              string
	Name              string
	CountExpression   string
	ForEachExpression string
}
