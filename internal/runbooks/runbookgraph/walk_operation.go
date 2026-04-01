// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

type WalkOperation byte

const (
	WalkInvalid WalkOperation = iota
	WalkValidate
	WalkPlan
	WalkExecute
)
