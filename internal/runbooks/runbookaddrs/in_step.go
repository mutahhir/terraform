// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"fmt"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/collections"
)

// InStep represents addresses of objects that belong to configuration
// constructs associated with a particular step.
//
// Although the type parameter is rather unconstrained, it doesn't make sense to
// use this for types other than the address types used for objects that can
// appear directly inside a runbook step.
type InStep[T InStepable] struct {
	Step ConfigStep
	Item T
}

// ConfigDataSource represents a data source configuration from inside a
// particular step.
type ConfigDataSource = InStep[addrs.ConfigResource]

// ConfigList represents a list block configuration from inside a particular
// step.
type ConfigList = InStep[addrs.ConfigResource]

// ConfigAction represents an action configuration from inside a particular
// step.
type ConfigAction = InStep[addrs.ConfigAction]

func (s InStep[T]) String() string {
	itemStr := s.Item.String()
	stepStr := s.Step.String()
	if itemStr == "" {
		return stepStr
	}
	return stepStr + "." + itemStr
}

// UniqueKey implements collections.UniqueKeyer.
func (s InStep[T]) UniqueKey() collections.UniqueKey[InStep[T]] {
	return inStepKey[T]{
		stepKey: s.Step.UniqueKey(),
		itemKey: s.Item.UniqueKey(),
	}
}

type inStepKey[T InStepable] struct {
	stepKey collections.UniqueKey[ConfigStep]
	itemKey addrs.UniqueKey
}

// IsUniqueKey implements collections.UniqueKey.
func (inStepKey[T]) IsUniqueKey(InStep[T]) {}

// InAbsStepInstance represents addresses of objects that belong to dynamic
// objects associated with a particular step instance.
//
// Although the type parameter is rather unconstrained, it doesn't make sense to
// use this for types other than the address types used for step-contained
// objects that can have dynamic instances.
type InAbsStepInstance[T InStepable] struct {
	Step StepInstance
	Item T
}

// AbsDataSource represents a not-yet-expanded data source from inside a
// particular step instance.
type AbsDataSource = InAbsStepInstance[addrs.AbsResource]

var _ collections.UniqueKeyer[AbsDataSource] = AbsDataSource{}

// AbsDataSourceInstance represents an instance of a data source from inside a
// particular step instance.
type AbsDataSourceInstance = InAbsStepInstance[addrs.AbsResourceInstance]

// AbsDataSourceInstanceObject represents an object associated with an instance
// of a data source from inside a particular step instance.
type AbsDataSourceInstanceObject = InAbsStepInstance[addrs.AbsResourceInstanceObject]

// AbsList represents a not-yet-expanded list block from inside a particular
// step instance.
type AbsList = InAbsStepInstance[addrs.AbsResource]

// AbsListInstance represents an instance of a list block from inside a
// particular step instance.
type AbsListInstance = InAbsStepInstance[addrs.AbsResourceInstance]

// AbsListInstanceObject represents an object associated with an instance of a
// list block from inside a particular step instance.
type AbsListInstanceObject = InAbsStepInstance[addrs.AbsResourceInstanceObject]

// AbsActionInvocationInstance represents an instance of an action from inside a
// particular step instance.
type AbsActionInvocationInstance = InAbsStepInstance[addrs.AbsActionInstance]

func (s InAbsStepInstance[T]) String() string {
	itemStr := s.Item.String()
	stepStr := s.Step.String()
	if itemStr == "" {
		return stepStr
	}
	return stepStr + "." + itemStr
}

// UniqueKey implements collections.UniqueKeyer.
func (s InAbsStepInstance[T]) UniqueKey() collections.UniqueKey[InAbsStepInstance[T]] {
	return inAbsStepInstanceKey[T]{
		stepKey: s.Step.UniqueKey(),
		itemKey: s.Item.UniqueKey(),
	}
}

type inAbsStepInstanceKey[T InStepable] struct {
	stepKey collections.UniqueKey[StepInstance]
	itemKey addrs.UniqueKey
}

// IsUniqueKey implements collections.UniqueKey.
func (inAbsStepInstanceKey[T]) IsUniqueKey(InAbsStepInstance[T]) {}

// InStepable just embeds the interfaces that we require for the type
// parameters of both the [InStep] and [InAbsStepInstance] types.
type InStepable interface {
	addrs.UniqueKeyer
	fmt.Stringer
}
