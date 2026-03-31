// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"reflect"

	"github.com/hashicorp/terraform/internal/collections"
)

// Referenceable is a type set containing all address types that can be the
// target of an expression-based reference within a runbook.
type Referenceable interface {
	referenceableSigil()
	String() string
}

var _ Referenceable = StepOutputValue{}
var _ Referenceable = WorkspaceActionInstance{}
var _ Referenceable = WorkspaceOutputValue{}

// ReferenceableUniqueKey returns a unique key for a dynamically-typed
// referenceable address.
func ReferenceableUniqueKey(addr Referenceable) collections.UniqueKey[Referenceable] {
	return referenceableUniqueKey{
		ty:  reflect.TypeOf(addr),
		str: addr.String(),
	}
}

type referenceableUniqueKey struct {
	ty  reflect.Type
	str string
}

// IsUniqueKey implements collections.UniqueKey.
func (referenceableUniqueKey) IsUniqueKey(Referenceable) {}
