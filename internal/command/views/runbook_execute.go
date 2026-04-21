package views

import (
	"fmt"
	"sort"
	"sync"

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
	Prepare(*runbookgraph.Plan)
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
	view           *View
	events         []runbookExecuteEvent
	actions        []runbookgraph.ActionExecEvent
	steps          []*runbookExecuteStepState
	stepStateByIdx map[int]*runbookExecuteStepState
	started        bool
	mu             sync.Mutex
}

type runbookExecuteEvent struct {
	StepName  string
	StepIndex int
	Status    runbookruntime.StepStatus
	Reason    string
}

type runbookExecuteStepState struct {
	Index            int
	Order            int
	Total            int
	Address          string
	Status           runbookruntime.StepStatus
	SkipReason       string
	RunningReported  bool
	TerminalReported bool
}

func (v *RunbookExecuteHuman) Diagnostics(diags tfdiags.Diagnostics) { v.view.Diagnostics(diags) }
func (v *RunbookExecuteHuman) HelpPrompt()                           { v.view.HelpPrompt("runbook execute") }
func (v *RunbookExecuteHuman) Prepare(plan *runbookgraph.Plan) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.started = false
	v.steps = nil
	v.stepStateByIdx = make(map[int]*runbookExecuteStepState)
	if plan == nil {
		return
	}
	for i, step := range plan.Steps {
		if step == nil {
			continue
		}
		status := step.Status
		if status == runbookruntime.StepStatusCompleted || status == runbookruntime.StepStatusPlanned || status == runbookruntime.StepStatusPending {
			status = runbookruntime.StepStatusPlanned
		}
		state := &runbookExecuteStepState{
			Index:      step.Index,
			Order:      i + 1,
			Total:      len(plan.Steps),
			Address:    formatRunbookRuntimeStepAddress(step),
			Status:     status,
			SkipReason: step.SkipReason,
		}
		v.steps = append(v.steps, state)
		v.stepStateByIdx[state.Index] = state
	}
}
func (v *RunbookExecuteHuman) UI() runbookgraph.UI                   { return v }
func (v *RunbookExecuteHuman) Hooks() []runbookgraph.Hook            { return nil }
func (v *RunbookExecuteHuman) PlannedStep(step *runbookruntime.Step) {}
func (v *RunbookExecuteHuman) PlannedStepInfo(info runbookgraph.StepPlanInfo) {
}
func (v *RunbookExecuteHuman) ActionEvent(event runbookgraph.ActionExecEvent) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.actions = append(v.actions, event)
	v.beginExecutionOutput()
	state := v.stepStateByIdx[event.StepIndex]
	if state != nil && !state.RunningReported && !state.TerminalReported {
		state.RunningReported = true
		state.Status = runbookruntime.StepStatusRunning
		v.view.streams.Printf("-> %s is running\n", v.renderStepLabel(state))
	}
	prefix := v.renderActionPrefix(event.StepIndex)
	switch event.Status {
	case "running":
		v.view.streams.Printf("  %saction %s is running\n", prefix, event.Subject)
	case "progress":
		v.view.streams.Printf("  %saction %s: %s\n", prefix, event.Subject, event.Message)
	case "completed":
		v.view.streams.Printf("  %saction %s completed\n", prefix, event.Subject)
	}
}
func (v *RunbookExecuteHuman) ExecutingStep(step *runbookruntime.Step) {
	if step == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	v.beginExecutionOutput()
	state := v.syncStepState(step)
	v.events = append(v.events, runbookExecuteEvent{StepName: step.Name, StepIndex: step.Index, Status: runbookruntime.StepStatusRunning})
	if state == nil || state.TerminalReported || state.RunningReported {
		return
	}
	state.RunningReported = true
	v.view.streams.Printf("-> %s is running\n", v.renderStepLabel(state))
}
func (v *RunbookExecuteHuman) ExecutedStep(step *runbookruntime.Step) {
	if step == nil || step.Status == runbookruntime.StepStatusRunning || step.Status == runbookruntime.StepStatusPlanned || step.Status == runbookruntime.StepStatusPending {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	v.beginExecutionOutput()
	state := v.stepStateByIdx[step.Index]
	alreadyReported := false
	previousStatus := runbookruntime.StepStatus("")
	previousReason := ""
	if state != nil {
		alreadyReported = state.TerminalReported
		previousStatus = state.Status
		previousReason = state.SkipReason
	}
	state = v.syncStepState(step)
	v.events = append(v.events, runbookExecuteEvent{StepName: step.Name, StepIndex: step.Index, Status: step.Status, Reason: step.SkipReason})
	if state == nil {
		return
	}
	if alreadyReported && previousStatus == state.Status && previousReason == state.SkipReason {
		return
	}
	if !state.RunningReported {
		state.RunningReported = true
		v.view.streams.Printf("-> %s is running\n", v.renderStepLabel(state))
	}
	state.TerminalReported = true
	switch step.Status {
	case runbookruntime.StepStatusCompleted:
		v.view.streams.Printf("ok %s completed\n", v.renderStepLabel(state))
	case runbookruntime.StepStatusSkipped:
		v.view.streams.Printf("sk %s skipped", v.renderStepLabel(state))
		if step.SkipReason != "" {
			v.view.streams.Printf(": %s", step.SkipReason)
		}
		v.view.streams.Print("\n")
	case runbookruntime.StepStatusFailed:
		v.view.streams.Printf("!! %s failed", v.renderStepLabel(state))
		if step.SkipReason != "" {
			v.view.streams.Printf(": %s", step.SkipReason)
		}
		v.view.streams.Print("\n")
	}
	v.view.streams.Printf("   %s\n", v.renderProgressSummary())
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
func (v *RunbookExecuteJSON) Prepare(plan *runbookgraph.Plan)       {}
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

func (v *RunbookExecuteHuman) beginExecutionOutput() {
	if v.started {
		return
	}
	v.started = true
	if len(v.steps) == 0 {
		v.view.streams.Println("Runbook execution started.")
		return
	}
	v.view.streams.Println("Runbook execution progress:")
	for _, step := range v.steps {
		v.view.streams.Printf("   %s %s\n", v.renderStepLabel(step), v.renderStepState(step))
	}
	v.view.streams.Printf("   %s\n\n", v.renderProgressSummary())
}

func (v *RunbookExecuteHuman) syncStepState(step *runbookruntime.Step) *runbookExecuteStepState {
	if step == nil {
		return nil
	}
	state, ok := v.stepStateByIdx[step.Index]
	if !ok {
		state = &runbookExecuteStepState{
			Index:   step.Index,
			Order:   len(v.steps) + 1,
			Total:   len(v.steps) + 1,
			Address: formatRunbookRuntimeStepAddress(step),
		}
		if v.stepStateByIdx == nil {
			v.stepStateByIdx = make(map[int]*runbookExecuteStepState)
		}
		v.stepStateByIdx[step.Index] = state
		v.steps = append(v.steps, state)
		for _, existing := range v.steps {
			existing.Total = len(v.steps)
		}
	}
	if !(state.TerminalReported && step.Status == runbookruntime.StepStatusRunning) {
		state.Status = step.Status
	}
	state.SkipReason = step.SkipReason
	state.Address = formatRunbookRuntimeStepAddress(step)
	return state
}

func (v *RunbookExecuteHuman) renderActionPrefix(stepIndex int) string {
	state, ok := v.stepStateByIdx[stepIndex]
	if !ok || state == nil {
		return ""
	}
	return fmt.Sprintf("[%d/%d] ", state.Order, state.Total)
}

func (v *RunbookExecuteHuman) renderStepLabel(step *runbookExecuteStepState) string {
	if step == nil {
		return "step"
	}
	return fmt.Sprintf("[%d/%d] %s", step.Order, step.Total, step.Address)
}

func (v *RunbookExecuteHuman) renderStepState(step *runbookExecuteStepState) string {
	if step == nil {
		return ""
	}
	switch step.Status {
	case runbookruntime.StepStatusCompleted:
		return "completed"
	case runbookruntime.StepStatusSkipped:
		if step.SkipReason != "" {
			return fmt.Sprintf("skipped: %s", step.SkipReason)
		}
		return "skipped"
	case runbookruntime.StepStatusFailed:
		if step.SkipReason != "" {
			return fmt.Sprintf("failed: %s", step.SkipReason)
		}
		return "failed"
	case runbookruntime.StepStatusRunning:
		return "running"
	default:
		return "waiting"
	}
}

func (v *RunbookExecuteHuman) renderProgressSummary() string {
	completed := 0
	running := 0
	waiting := 0
	skipped := 0
	failed := 0
	for _, step := range v.steps {
		switch step.Status {
		case runbookruntime.StepStatusCompleted:
			completed++
		case runbookruntime.StepStatusRunning:
			running++
		case runbookruntime.StepStatusSkipped:
			skipped++
		case runbookruntime.StepStatusFailed:
			failed++
		default:
			waiting++
		}
	}
	return fmt.Sprintf("Progress: %d completed, %d running, %d waiting, %d skipped, %d failed.", completed, running, waiting, skipped, failed)
}

func formatRunbookRuntimeStepAddress(step *runbookruntime.Step) string {
	if step == nil {
		return "step"
	}
	if step.InstanceKey == nil {
		return fmt.Sprintf("step.%s", step.Name)
	}
	return fmt.Sprintf("step.%s%s", step.Name, step.InstanceKey.String())
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
