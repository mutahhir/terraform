package views

import (
	"fmt"
	"sort"
	"strings"
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
	liveMode       bool
	renderedLines  int
	lastActiveStep int
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
	LogLines         []string
}

const runbookStepLogRetention = 200

func (v *RunbookExecuteHuman) Diagnostics(diags tfdiags.Diagnostics) { v.view.Diagnostics(diags) }
func (v *RunbookExecuteHuman) HelpPrompt()                           { v.view.HelpPrompt("runbook execute") }
func (v *RunbookExecuteHuman) Prepare(plan *runbookgraph.Plan) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.started = false
	v.liveMode = v.view.streams.Stdout.IsTerminal()
	v.renderedLines = 0
	v.lastActiveStep = 0
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
	if len(v.steps) > 0 {
		v.lastActiveStep = v.steps[0].Index
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
	if state != nil {
		v.lastActiveStep = state.Index
	}
	if state != nil && !state.RunningReported && !state.TerminalReported {
		state.RunningReported = true
		state.Status = runbookruntime.StepStatusRunning
		if v.liveMode {
			v.appendStepLog(state, fmt.Sprintf("%s is running", state.Address))
		} else {
			v.view.streams.Printf("-> %s is running\n", v.renderStepLabel(state))
		}
	}
	prefix := v.renderActionPrefix(event.StepIndex)
	switch event.Status {
	case "running":
		if v.liveMode {
			v.appendStepLog(state, fmt.Sprintf("action %s is running", event.Subject))
		} else {
			v.view.streams.Printf("  %saction %s is running\n", prefix, event.Subject)
		}
	case "progress":
		if v.liveMode {
			v.appendStepLog(state, fmt.Sprintf("action %s: %s", event.Subject, event.Message))
		} else {
			v.view.streams.Printf("  %saction %s: %s\n", prefix, event.Subject, event.Message)
		}
	case "completed":
		if v.liveMode {
			v.appendStepLog(state, fmt.Sprintf("action %s completed", event.Subject))
		} else {
			v.view.streams.Printf("  %saction %s completed\n", prefix, event.Subject)
		}
	}
	if v.liveMode {
		v.redrawLive()
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
		if v.liveMode {
			v.redrawLive()
		}
		return
	}
	state.RunningReported = true
	v.lastActiveStep = state.Index
	if v.liveMode {
		v.appendStepLog(state, fmt.Sprintf("%s is running", state.Address))
		v.redrawLive()
		return
	}
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
	v.lastActiveStep = state.Index
	if alreadyReported && previousStatus == state.Status && previousReason == state.SkipReason {
		if v.liveMode {
			v.redrawLive()
		}
		return
	}
	if !state.RunningReported {
		state.RunningReported = true
		if v.liveMode {
			v.appendStepLog(state, fmt.Sprintf("%s is running", state.Address))
		} else {
			v.view.streams.Printf("-> %s is running\n", v.renderStepLabel(state))
		}
	}
	state.TerminalReported = true
	switch step.Status {
	case runbookruntime.StepStatusCompleted:
		if v.liveMode {
			v.appendStepLog(state, fmt.Sprintf("%s completed", state.Address))
		} else {
			v.view.streams.Printf("ok %s completed\n", v.renderStepLabel(state))
		}
	case runbookruntime.StepStatusSkipped:
		if v.liveMode {
			message := fmt.Sprintf("%s skipped", state.Address)
			if step.SkipReason != "" {
				message = fmt.Sprintf("%s: %s", message, step.SkipReason)
			}
			v.appendStepLog(state, message)
		} else {
			v.view.streams.Printf("sk %s skipped", v.renderStepLabel(state))
			if step.SkipReason != "" {
				v.view.streams.Printf(": %s", step.SkipReason)
			}
			v.view.streams.Print("\n")
		}
	case runbookruntime.StepStatusFailed:
		if v.liveMode {
			message := fmt.Sprintf("%s failed", state.Address)
			if step.SkipReason != "" {
				message = fmt.Sprintf("%s: %s", message, step.SkipReason)
			}
			v.appendStepLog(state, message)
		} else {
			v.view.streams.Printf("!! %s failed", v.renderStepLabel(state))
			if step.SkipReason != "" {
				v.view.streams.Printf(": %s", step.SkipReason)
			}
			v.view.streams.Print("\n")
		}
	}
	if v.liveMode {
		v.redrawLive()
		return
	}
	v.view.streams.Printf("   %s\n", v.renderProgressSummary())
}
func (v *RunbookExecuteHuman) Executed(plan *runbookgraph.Plan) {
	v.mu.Lock()
	if v.liveMode {
		v.redrawLive()
		v.renderedLines = 0
		v.view.streams.Print("\n")
	}
	v.mu.Unlock()

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
	if v.liveMode {
		v.redrawLive()
		return
	}
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

func (v *RunbookExecuteHuman) appendStepLog(step *runbookExecuteStepState, line string) {
	if step == nil || line == "" {
		return
	}
	step.LogLines = append(step.LogLines, line)
	if len(step.LogLines) > runbookStepLogRetention {
		step.LogLines = append([]string(nil), step.LogLines[len(step.LogLines)-runbookStepLogRetention:]...)
	}
}

func (v *RunbookExecuteHuman) redrawLive() {
	block := v.renderLiveBlock()
	if v.renderedLines > 0 {
		v.view.streams.Printf("\x1b[%dA\r", v.renderedLines)
		v.view.streams.Print("\x1b[0m\x1b[J")
	} else {
		v.view.streams.Print("\x1b[0m")
	}
	v.view.streams.Print(block)
	v.renderedLines = strings.Count(block, "\n")
}

func (v *RunbookExecuteHuman) renderLiveBlock() string {
	width := v.view.outputColumns()
	if width < 40 {
		width = 40
	}
	separator := strings.Repeat("-", width)
	var lines []string
	lines = append(lines, "Runbook execution")
	lines = append(lines, separator)
	lines = append(lines, "Steps")
	for _, line := range v.renderLiveSteps(width) {
		lines = append(lines, line)
	}
	lines = append(lines, separator)
	for _, line := range v.renderLiveLogs(width) {
		lines = append(lines, line)
	}
	lines = append(lines, separator)
	for _, line := range v.renderLiveFooter(width) {
		lines = append(lines, line)
	}
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		wrapped = append(wrapped, v.wrapLiveLines(line, width)...)
	}
	return strings.Join(wrapped, "\n") + "\n"
}

func (v *RunbookExecuteHuman) renderLiveSteps(width int) []string {
	if len(v.steps) == 0 {
		return []string{"  (no steps)"}
	}
	focusOrder := v.focusedStepOrder()
	const windowSize = 8
	start := maxInt(0, focusOrder-1-windowSize/2)
	end := minInt(len(v.steps), start+windowSize)
	if end-start < windowSize {
		start = maxInt(0, end-windowSize)
	}
	ret := make([]string, 0, end-start+2)
	if start > 0 {
		ret = append(ret, fmt.Sprintf("  ... %d earlier step(s)", start))
	}
	for i := start; i < end; i++ {
		step := v.steps[i]
		cursor := " "
		if step.Index == v.lastActiveStep {
			cursor = ">"
		}
		ret = append(ret, fmt.Sprintf("%s %-9s %s", cursor, liveStatusLabel(step), step.Address))
	}
	if end < len(v.steps) {
		ret = append(ret, fmt.Sprintf("  ... %d later step(s)", len(v.steps)-end))
	}
	return ret
}

func (v *RunbookExecuteHuman) renderLiveLogs(width int) []string {
	step := v.stepStateByIdx[v.lastActiveStep]
	if step == nil && len(v.steps) > 0 {
		step = v.steps[0]
	}
	if step == nil {
		return []string{"Step output", "  (no output yet)"}
	}
	ret := []string{fmt.Sprintf("Step output: %s", step.Address)}
	if len(step.LogLines) == 0 {
		return append(ret, "  (waiting for output)")
	}
	const visibleLogs = 14
	start := maxInt(0, len(step.LogLines)-visibleLogs)
	if start > 0 {
		ret = append(ret, fmt.Sprintf("  ... %d earlier line(s)", start))
	}
	for _, line := range step.LogLines[start:] {
		ret = append(ret, "  "+line)
	}
	return ret
}

func (v *RunbookExecuteHuman) focusedStepOrder() int {
	if step := v.stepStateByIdx[v.lastActiveStep]; step != nil {
		return step.Order
	}
	for _, step := range v.steps {
		if step.Status == runbookruntime.StepStatusRunning {
			return step.Order
		}
		if step.Status == runbookruntime.StepStatusPlanned || step.Status == runbookruntime.StepStatusPending {
			return step.Order
		}
	}
	if len(v.steps) == 0 {
		return 1
	}
	return v.steps[len(v.steps)-1].Order
}

func (v *RunbookExecuteHuman) renderLiveFooter(width int) []string {
	step := v.stepStateByIdx[v.lastActiveStep]
	if step == nil && len(v.steps) > 0 {
		step = v.steps[0]
	}
	current := "Current step: none"
	if step != nil {
		current = fmt.Sprintf("Current step: %s (%s)", step.Address, strings.Trim(liveStatusLabel(step), "[]"))
	}
	return []string{current, v.renderProgressSummary()}
}

func (v *RunbookExecuteHuman) wrapLiveLines(line string, width int) []string {
	if width <= 0 {
		return []string{line}
	}
	if line == "" {
		return []string{""}
	}
	indent := leadingIndent(line)
	remaining := []rune(line)
	var lines []string
	for len(remaining) > 0 {
		if len(remaining) <= width {
			lines = append(lines, string(remaining))
			break
		}
		cut := width
		segment := remaining[:cut]
		split := -1
		for i := len(segment) - 1; i >= 0; i-- {
			if segment[i] == ' ' {
				split = i
				break
			}
		}
		if split <= indent || split == -1 {
			split = cut
			lines = append(lines, string(remaining[:split]))
			remaining = append([]rune(strings.Repeat(" ", indent)), remaining[split:]...)
			continue
		}
		lines = append(lines, string(remaining[:split]))
		remaining = append([]rune(strings.Repeat(" ", indent)), trimLeadingSpacesRunes(remaining[split:])...)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func leadingIndent(line string) int {
	count := 0
	for _, r := range line {
		if r != ' ' {
			break
		}
		count++
	}
	return count
}

func trimLeadingSpacesRunes(in []rune) []rune {
	idx := 0
	for idx < len(in) && in[idx] == ' ' {
		idx++
	}
	return in[idx:]
}

func liveStatusLabel(step *runbookExecuteStepState) string {
	if step == nil {
		return "[unknown]"
	}
	switch step.Status {
	case runbookruntime.StepStatusCompleted:
		return "[done]"
	case runbookruntime.StepStatusSkipped:
		return "[skipped]"
	case runbookruntime.StepStatusFailed:
		return "[failed]"
	case runbookruntime.StepStatusRunning:
		return "[running]"
	default:
		return "[waiting]"
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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
