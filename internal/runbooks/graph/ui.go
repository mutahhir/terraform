package runbookgraph

import (
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/zclconf/go-cty/cty"
)

type UI interface {
	PlannedStep(*runbookruntime.Step)
	PlannedStepInfo(StepPlanInfo)
}

type Hook interface {
	PlannedStep(*runbookruntime.Step)
	PlannedStepInfo(StepPlanInfo)
}

type StepPlanInfo struct {
	StepName  string
	StepIndex int
	Type      string
	Subject   string
	Status    runbookruntime.StepStatus
	Value     cty.Value
}
