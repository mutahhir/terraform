package views

import (
	"fmt"
	"sort"

	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/repl"
	runbookgraph "github.com/hashicorp/terraform/internal/runbooks/graph"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

type RunbookExecute interface {
	Diagnostics(tfdiags.Diagnostics)
	HelpPrompt()
	UI() runbookgraph.UI
	Hooks() []runbookgraph.Hook
	Executed(*runbookgraph.Plan)
}

func NewRunbookExecute(vt arguments.ViewType, view *View) RunbookExecute {
	switch vt {
	case arguments.ViewJSON:
		return &RunbookExecuteJSON{view: NewJSONView(view)}
	case arguments.ViewHuman:
		return &RunbookExecuteHuman{view: view}
	default:
		panic(fmt.Sprintf("unknown view type %v", vt))
	}
}

type RunbookExecuteHuman struct {
	view    *View
	events  []runbookExecuteEvent
	actions []runbookgraph.ActionExecEvent
}

type runbookExecuteEvent struct {
	StepName  string
	StepIndex int
	Status    runbookruntime.StepStatus
	Reason    string
}

func (v *RunbookExecuteHuman) Diagnostics(diags tfdiags.Diagnostics) { v.view.Diagnostics(diags) }
func (v *RunbookExecuteHuman) HelpPrompt()                           { v.view.HelpPrompt("runbook execute") }
func (v *RunbookExecuteHuman) UI() runbookgraph.UI                   { return v }
func (v *RunbookExecuteHuman) Hooks() []runbookgraph.Hook            { return nil }
func (v *RunbookExecuteHuman) PlannedStep(step *runbookruntime.Step) {}
func (v *RunbookExecuteHuman) PlannedStepInfo(info runbookgraph.StepPlanInfo) {
}
func (v *RunbookExecuteHuman) ActionEvent(event runbookgraph.ActionExecEvent) {
	v.actions = append(v.actions, event)
	switch event.Status {
	case "running":
		v.view.streams.Printf("  action %s is running\n", event.Subject)
	case "progress":
		v.view.streams.Printf("  action %s: %s\n", event.Subject, event.Message)
	case "completed":
		v.view.streams.Printf("  action %s completed\n", event.Subject)
	}
}
func (v *RunbookExecuteHuman) ExecutingStep(step *runbookruntime.Step) {
	if step == nil {
		return
	}
	v.events = append(v.events, runbookExecuteEvent{StepName: step.Name, StepIndex: step.Index, Status: runbookruntime.StepStatusRunning})
	v.view.streams.Printf("step.%s is running\n", step.Name)
}
func (v *RunbookExecuteHuman) ExecutedStep(step *runbookruntime.Step) {
	if step == nil || step.Status == runbookruntime.StepStatusRunning || step.Status == runbookruntime.StepStatusPlanned || step.Status == runbookruntime.StepStatusPending {
		return
	}
	v.events = append(v.events, runbookExecuteEvent{StepName: step.Name, StepIndex: step.Index, Status: step.Status, Reason: step.SkipReason})
	switch step.Status {
	case runbookruntime.StepStatusCompleted:
		v.view.streams.Printf("step.%s completed\n", step.Name)
	case runbookruntime.StepStatusSkipped:
		v.view.streams.Printf("step.%s skipped", step.Name)
		if step.SkipReason != "" {
			v.view.streams.Printf(": %s", step.SkipReason)
		}
		v.view.streams.Print("\n")
	case runbookruntime.StepStatusFailed:
		v.view.streams.Printf("step.%s failed", step.Name)
		if step.SkipReason != "" {
			v.view.streams.Printf(": %s", step.SkipReason)
		}
		v.view.streams.Print("\n")
	}
}
func (v *RunbookExecuteHuman) Executed(plan *runbookgraph.Plan) {
	steps := 0
	skipped := 0
	failed := 0
	for _, step := range plan.Steps {
		switch step.Status {
		case runbookruntime.StepStatusSkipped:
			skipped++
		case runbookruntime.StepStatusFailed:
			failed++
		default:
			steps++
		}
	}
	v.view.streams.Printf("Runbook execute complete. Steps: %d completed, %d skipped, %d failed.\n", steps, skipped, failed)
	outputs := collectRunbookExecuteOutputs(plan)
	if len(outputs) == 0 {
		return
	}
	v.view.streams.Print("\nOutputs:\n")
	keys := make([]string, 0, len(outputs))
	for key := range outputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		v.view.streams.Printf("%s = %s\n", key, repl.FormatValue(outputs[key], 0))
	}
}

type RunbookExecuteJSON struct {
	view    *JSONView
	events  []map[string]any
	actions []map[string]any
}

func (v *RunbookExecuteJSON) Diagnostics(diags tfdiags.Diagnostics) { v.view.Diagnostics(diags) }
func (v *RunbookExecuteJSON) HelpPrompt()                           {}
func (v *RunbookExecuteJSON) UI() runbookgraph.UI                   { return v }
func (v *RunbookExecuteJSON) Hooks() []runbookgraph.Hook            { return nil }
func (v *RunbookExecuteJSON) PlannedStep(step *runbookruntime.Step) {}
func (v *RunbookExecuteJSON) PlannedStepInfo(info runbookgraph.StepPlanInfo) {
}
func (v *RunbookExecuteJSON) ActionEvent(event runbookgraph.ActionExecEvent) {
	v.actions = append(v.actions, map[string]any{
		"step_name":   event.StepName,
		"step_index":  event.StepIndex,
		"subject":     event.Subject,
		"action_type": event.ActionType,
		"status":      event.Status,
		"message":     event.Message,
	})
}
func (v *RunbookExecuteJSON) ExecutingStep(step *runbookruntime.Step) {
	if step == nil {
		return
	}
	v.events = append(v.events, map[string]any{"step_name": step.Name, "step_index": step.Index, "status": string(runbookruntime.StepStatusRunning)})
}
func (v *RunbookExecuteJSON) ExecutedStep(step *runbookruntime.Step) {
	if step == nil || step.Status == runbookruntime.StepStatusRunning || step.Status == runbookruntime.StepStatusPlanned || step.Status == runbookruntime.StepStatusPending {
		return
	}
	v.events = append(v.events, map[string]any{"step_name": step.Name, "step_index": step.Index, "status": string(step.Status), "reason": step.SkipReason})
}
func (v *RunbookExecuteJSON) Executed(plan *runbookgraph.Plan) {
	sort.SliceStable(v.events, func(i, j int) bool {
		if v.events[i]["step_index"] == v.events[j]["step_index"] {
			return fmt.Sprintf("%v", v.events[i]["status"]) < fmt.Sprintf("%v", v.events[j]["status"])
		}
		return v.events[i]["step_index"].(int) < v.events[j]["step_index"].(int)
	})
	v.view.log.Info("Runbook execute", "type", "runbook_execute", "plan", buildRunbookPlan(plan, nil), "events", v.events, "actions", v.actions)
}

func collectRunbookExecuteOutputs(plan *runbookgraph.Plan) map[string]cty.Value {
	if plan == nil {
		return nil
	}
	outputs := map[string]cty.Value{}
	for _, step := range plan.Steps {
		if step == nil || step.Outputs == cty.NilVal || !step.Outputs.IsKnown() || step.Outputs.IsNull() || !step.Outputs.Type().IsObjectType() {
			continue
		}
		stepAddr := fmt.Sprintf("step.%s", step.Name)
		if step.InstanceKey != nil {
			stepAddr = fmt.Sprintf("step.%s%s", step.Name, step.InstanceKey.String())
		}
		for name, value := range step.Outputs.AsValueMap() {
			outputs[fmt.Sprintf("%s.%s", stepAddr, name)] = value
		}
	}
	return outputs
}
