package views

import (
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform/internal/command/arguments"
	runbookgraph "github.com/hashicorp/terraform/internal/runbooks/graph"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

type RunbookPlan interface {
	UI() runbookgraph.UI
	Hooks() []runbookgraph.Hook
	Diagnostics(tfdiags.Diagnostics)
	HelpPrompt()
	Plan(*runbookgraph.Plan)
}

type runbookPlan struct {
	Steps []runbookPlanStep `json:"steps"`
	Info  []runbookPlanInfo `json:"info"`
}

type runbookPlanStep struct {
	Name       string                     `json:"name"`
	Index      int                        `json:"index"`
	Status     runbookruntime.StepStatus  `json:"status"`
	SkipReason string                     `json:"skip_reason,omitempty"`
	Outputs    map[string]json.RawMessage `json:"outputs,omitempty"`
}

type runbookPlanInfo struct {
	StepName  string                    `json:"step_name"`
	StepIndex int                       `json:"step_index"`
	Type      string                    `json:"type"`
	Subject   string                    `json:"subject"`
	Status    runbookruntime.StepStatus `json:"status"`
}

func NewRunbookPlan(vt arguments.ViewType, view *View) RunbookPlan {
	switch vt {
	case arguments.ViewJSON:
		return &RunbookPlanJSON{view: NewJSONView(view)}
	case arguments.ViewHuman:
		return &RunbookPlanHuman{view: view}
	default:
		panic(fmt.Sprintf("unknown view type %v", vt))
	}
}

type RunbookPlanHuman struct {
	view *View
	info []runbookPlanInfo
}

func (v *RunbookPlanHuman) UI() runbookgraph.UI                   { return v }
func (v *RunbookPlanHuman) Hooks() []runbookgraph.Hook            { return []runbookgraph.Hook{v} }
func (v *RunbookPlanHuman) Diagnostics(diags tfdiags.Diagnostics) { v.view.Diagnostics(diags) }
func (v *RunbookPlanHuman) HelpPrompt()                           { v.view.HelpPrompt("runbook plan") }
func (v *RunbookPlanHuman) PlannedStep(step *runbookruntime.Step) {}
func (v *RunbookPlanHuman) PlannedStepInfo(info runbookgraph.StepPlanInfo) {
	v.info = append(v.info, runbookPlanInfo(info))
}
func (v *RunbookPlanHuman) Plan(plan *runbookgraph.Plan) {
	viewPlan := buildRunbookPlan(plan, v.info)
	for _, step := range viewPlan.Steps {
		line := fmt.Sprintf("step.%s[%d]: %s", step.Name, step.Index, step.Status)
		if step.SkipReason != "" {
			line += fmt.Sprintf(" (%s)", step.SkipReason)
		}
		v.view.streams.Println(line)
	}
	for _, info := range viewPlan.Info {
		v.view.streams.Println(fmt.Sprintf("  - %s: %s", info.Type, info.Subject))
	}
}

type RunbookPlanJSON struct {
	view *JSONView
	info []runbookPlanInfo
}

func (v *RunbookPlanJSON) UI() runbookgraph.UI                   { return v }
func (v *RunbookPlanJSON) Hooks() []runbookgraph.Hook            { return []runbookgraph.Hook{v} }
func (v *RunbookPlanJSON) Diagnostics(diags tfdiags.Diagnostics) { v.view.Diagnostics(diags) }
func (v *RunbookPlanJSON) HelpPrompt()                           {}
func (v *RunbookPlanJSON) PlannedStep(step *runbookruntime.Step) {}
func (v *RunbookPlanJSON) PlannedStepInfo(info runbookgraph.StepPlanInfo) {
	v.info = append(v.info, runbookPlanInfo(info))
}
func (v *RunbookPlanJSON) Plan(plan *runbookgraph.Plan) {
	v.view.log.Info("Runbook plan", "type", "runbook_plan", "plan", buildRunbookPlan(plan, v.info))
}

func buildRunbookPlan(plan *runbookgraph.Plan, info []runbookPlanInfo) runbookPlan {
	ret := runbookPlan{Info: append([]runbookPlanInfo(nil), info...)}
	if plan == nil {
		return ret
	}
	ret.Steps = make([]runbookPlanStep, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		ret.Steps = append(ret.Steps, toRunbookPlanStep(step))
	}
	return ret
}

func toRunbookPlanStep(step *runbookruntime.Step) runbookPlanStep {
	ret := runbookPlanStep{
		Name:       step.Name,
		Index:      step.Index,
		Status:     step.Status,
		SkipReason: step.SkipReason,
	}
	if step.Outputs != cty.NilVal && step.Outputs.IsKnown() && !step.Outputs.IsNull() && step.Outputs.Type().IsObjectType() {
		ret.Outputs = make(map[string]json.RawMessage)
		for name, value := range step.Outputs.AsValueMap() {
			encoded, err := json.Marshal(value.GoString())
			if err == nil {
				ret.Outputs[name] = encoded
			}
		}
	}
	return ret
}
