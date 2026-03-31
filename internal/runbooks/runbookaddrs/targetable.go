// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"github.com/hashicorp/terraform/internal/addrs"
)

// Targetable is the stacks analog to [addrs.Targetable], representing something
// that can be "targeted" inside a runbook configuration.
type Targetable interface {
	targetableSigil()
}

// StepTargetable is an adapter type that makes everything that's targetable in
// the main Terraform language also targetable through a step instance when in a
// runbook configuration.
type StepTargetable[T addrs.Targetable] struct {
	StepInstance StepInstance
	Item         T
}

func (StepTargetable[T]) targetableSigil() {}
