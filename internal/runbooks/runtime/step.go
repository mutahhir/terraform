package runbookruntime

import (
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/terraform"
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

	Name           string
	Index          int
	InstanceKey    terraformaddrs.InstanceKey
	RepetitionData *terraform.InstanceKeyEvalData

	Status     StepStatus
	SkipReason string

	Outputs cty.Value
}
