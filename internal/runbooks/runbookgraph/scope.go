// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

type Scope interface {
	String() string
}

type RootScope struct{}

func (RootScope) String() string { return "runbook.root" }

type StepConfigScope struct {
	StepName string
}

func (s StepConfigScope) String() string {
	return "step." + s.StepName
}

type StepInstanceScope struct {
	StepAddress string
}

func (s StepInstanceScope) String() string {
	return s.StepAddress
}
