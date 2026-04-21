package runbookgraph

import (
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/zclconf/go-cty/cty"
)

type UI interface {
	PlannedStep(*runbookruntime.Step)
	PlannedStepInfo(StepPlanInfo)
	ExecutingStep(*runbookruntime.Step)
	ExecutedStep(*runbookruntime.Step)
	ActionEvent(ActionExecEvent)
}

type Hook interface {
	PlannedStep(*runbookruntime.Step)
	PlannedStepInfo(StepPlanInfo)
	ExecutingStep(*runbookruntime.Step)
	ExecutedStep(*runbookruntime.Step)
	ActionEvent(ActionExecEvent)
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
