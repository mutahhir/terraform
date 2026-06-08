package runbookgraph

import (
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/zclconf/go-cty/cty"
)

// HookAction is the return value from hook methods that determines whether
// execution should continue or halt.
type HookAction byte

const (
	// HookActionContinue continues with processing as usual.
	HookActionContinue HookAction = iota

	// HookActionHalt halts immediately: no more hooks are processed
	// and the current operation is cancelled.
	HookActionHalt
)

// UI receives notifications about runbook execution progress.
// UI methods are fire-and-forget (no control flow).
type UI interface {
	PlannedStep(*runbookruntime.Step)
	PlannedStepInfo(StepPlanInfo)
	ExecutingStep(*runbookruntime.Step)
	ExecutedStep(*runbookruntime.Step)
	ActionEvent(ActionExecEvent)
	CatchTriggered(catchName string, failedStepName string)
	CatchCompleted(catchName string, failedStepName string)
	CatchSkipped(catchName string, reason string)
	CatchFailed(catchName string, err string)
}

// Hook allows external observers to monitor and control runbook execution.
// Each method returns a HookAction that can halt execution, matching
// Terraform core's Hook interface pattern.
type Hook interface {
	PlannedStep(*runbookruntime.Step) (HookAction, error)
	PlannedStepInfo(StepPlanInfo) (HookAction, error)
	ExecutingStep(*runbookruntime.Step) (HookAction, error)
	ExecutedStep(*runbookruntime.Step) (HookAction, error)
	ActionEvent(ActionExecEvent) (HookAction, error)
	CatchTriggered(catchName string, failedStepName string) (HookAction, error)
	CatchCompleted(catchName string, failedStepName string) (HookAction, error)
	CatchSkipped(catchName string, reason string) (HookAction, error)
	CatchFailed(catchName string, err string) (HookAction, error)
}

type StepPlanInfo struct {
	StepName  string
	StepIndex int
	Type      string
	Subject   string
	Status    runbookruntime.StepStatus
	Value     cty.Value
	Details   cty.Value
}

type ActionExecEvent struct {
	StepName   string
	StepIndex  int
	Subject    string
	ActionType string
	Status     string
	Message    string
}
