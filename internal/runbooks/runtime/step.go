package runbookruntime

import (
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/zclconf/go-cty/cty"
)

type StepStatus string

const (
	StepStatusPending   StepStatus = "pending"
	StepStatusPlanned   StepStatus = "planned"
	StepStatusRunning   StepStatus = "running"
	StepStatusCompleted StepStatus = "completed"
	StepStatusSkipped   StepStatus = "skipped"
	StepStatusFailed    StepStatus = "failed"
)

type Step struct {
	Config *runbookconfigs.Step

	Name  string
	Index int

	Status     StepStatus
	SkipReason string

	Outputs cty.Value
}
