package runbookplanfile

import (
	"github.com/hashicorp/terraform/internal/plans"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
)

const FormatVersion = 1

type Plan struct {
	Version           int                           `json:"version"`
	TerraformVersion  string                        `json:"terraform_version,omitempty"`
	RunbookSourceDir  string                        `json:"runbook_source_dir,omitempty"`
	Sources           map[string][]byte             `json:"sources,omitempty"`
	RunbookLockFile   []byte                        `json:"runbook_lock_file,omitempty"`
	TerraformLockFile []byte                        `json:"terraform_lock_file,omitempty"`
	Variables         map[string]plans.DynamicValue `json:"variables,omitempty"`
	Steps             []*Step                       `json:"steps,omitempty"`
	PlanInfo          []*StepPlanInfo               `json:"plan_info,omitempty"`
}

type Step struct {
	Name           string                        `json:"name"`
	Index          int                           `json:"index"`
	Status         runbookruntime.StepStatus     `json:"status,omitempty"`
	SkipReason     string                        `json:"skip_reason,omitempty"`
	InstanceKey    *InstanceKey                  `json:"instance_key,omitempty"`
	RepetitionData *RepetitionData               `json:"repetition_data,omitempty"`
	Data           map[string]plans.DynamicValue `json:"data,omitempty"`
	Lists          map[string]plans.DynamicValue `json:"lists,omitempty"`
	Outputs        map[string]plans.DynamicValue `json:"outputs,omitempty"`
	Actions        map[string]*ActionState       `json:"actions,omitempty"`
}

type InstanceKey struct {
	Kind string `json:"kind,omitempty"`
	Str  string `json:"str,omitempty"`
	Int  int    `json:"int,omitempty"`
}

type RepetitionData struct {
	CountIndex *int64             `json:"count_index,omitempty"`
	EachKey    string             `json:"each_key,omitempty"`
	EachValue  plans.DynamicValue `json:"each_value,omitempty"`
}

type ActionState struct {
	Planned       bool               `json:"planned,omitempty"`
	Invoked       bool               `json:"invoked,omitempty"`
	PlannedConfig plans.DynamicValue `json:"planned_config,omitempty"`
}

type StepPlanInfo struct {
	StepName  string                    `json:"step_name,omitempty"`
	StepIndex int                       `json:"step_index,omitempty"`
	Type      string                    `json:"type,omitempty"`
	Subject   string                    `json:"subject,omitempty"`
	Status    runbookruntime.StepStatus `json:"status,omitempty"`
	Value     plans.DynamicValue        `json:"value,omitempty"`
	Details   plans.DynamicValue        `json:"details,omitempty"`
}
