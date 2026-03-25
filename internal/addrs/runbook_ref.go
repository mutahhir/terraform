// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package addrs

import "fmt"

// Step is the address of a runbook step declaration, which may have multiple
// instances if it uses count or for_each.
type Step struct {
	referenceable
	Name string
}

func (s Step) String() string {
	return fmt.Sprintf("steps.%s", s.Name)
}

func (s Step) UniqueKey() UniqueKey {
	return s
}

func (s Step) uniqueKeySigil() {}

func (s Step) Instance(key InstanceKey) StepInstance {
	return StepInstance{Step: s, Key: key}
}

// StepInstance is the address of a concrete runbook step instance.
type StepInstance struct {
	referenceable
	Step Step
	Key  InstanceKey
}

func (s StepInstance) String() string {
	if s.Key == NoKey {
		return s.Step.String()
	}
	return s.Step.String() + s.Key.String()
}

func (s StepInstance) UniqueKey() UniqueKey {
	return s
}

func (s StepInstance) uniqueKeySigil() {}

// WorkspaceOutput is the address of a workspace output reference in runbooks.
type WorkspaceOutput struct {
	referenceable
	Name string
}

func (w WorkspaceOutput) String() string {
	return fmt.Sprintf("workspace.output.%s", w.Name)
}

func (w WorkspaceOutput) UniqueKey() UniqueKey {
	return w
}

func (w WorkspaceOutput) uniqueKeySigil() {}

// RunbookAction is the address of a runbook action reference in runbooks.
type RunbookAction struct {
	referenceable
	Type string
	Name string
}

func (a RunbookAction) String() string {
	return fmt.Sprintf("action.%s.%s", a.Type, a.Name)
}

func (a RunbookAction) UniqueKey() UniqueKey {
	return a
}

func (a RunbookAction) uniqueKeySigil() {}
