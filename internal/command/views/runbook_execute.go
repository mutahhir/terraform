package views

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

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
	view             *View
	events           []runbookExecuteEvent
	actions          []runbookgraph.ActionExecEvent
	steps            []*runbookExecuteStepState
	stepStateByAddr  map[string]*runbookExecuteStepState
	stepStatesByIdx  map[int][]*runbookExecuteStepState
	stepStatesByName map[string][]*runbookExecuteStepState
	logs             []string
	started          bool
	liveMode         bool
	renderedLines    int
	spinnerIndex     int
	spinnerStop      chan struct{}
	mu               sync.Mutex
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

type runbookRenderLine struct {
	Text  string
	Green bool
}

const (
	runbookExecuteLogRetention   = 200
	runbookExecuteVisibleLogs    = 16
	runbookExecuteSpinnerEvery   = 120 * time.Millisecond
	runbookExecuteMinimumWidth   = 40
	runbookExecuteSectionDivider = "-"
)

var (
	runbookExecuteSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	runbookANSIEscapeRE         = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
)

func (v *RunbookExecuteHuman) Diagnostics(diags tfdiags.Diagnostics) { v.view.Diagnostics(diags) }
func (v *RunbookExecuteHuman) HelpPrompt()                           { v.view.HelpPrompt("runbook execute") }
func (v *RunbookExecuteHuman) Prepare(plan *runbookgraph.Plan) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.stopSpinnerLoopLocked()
	v.started = false
	v.liveMode = v.view.streams.Stdout.IsTerminal()
	v.renderedLines = 0
	v.spinnerIndex = 0
	v.logs = nil
	v.steps = nil
	v.stepStateByAddr = make(map[string]*runbookExecuteStepState)
	v.stepStatesByIdx = make(map[int][]*runbookExecuteStepState)
	v.stepStatesByName = make(map[string][]*runbookExecuteStepState)
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
		v.stepStateByAddr[state.Address] = state
		v.stepStatesByIdx[state.Index] = append(v.stepStatesByIdx[state.Index], state)
		v.stepStatesByName[step.Name] = append(v.stepStatesByName[step.Name], state)
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
	v.beginExecutionOutputLocked()
	state := v.findStepStateForEventLocked(event.StepName, event.StepIndex)
	if state != nil && !state.RunningReported && !state.TerminalReported {
		state.RunningReported = true
		state.Status = runbookruntime.StepStatusRunning
		v.emitLogLocked(fmt.Sprintf("-> %s is in progress", v.renderStepLabel(state)))
	}

	prefix := v.renderActionPrefix(event.StepIndex)
	switch event.Status {
	case "running":
		v.emitLogLocked(fmt.Sprintf("  %saction %s is running", prefix, event.Subject))
	case "progress":
		if event.Message != "" {
			v.emitLogLocked(fmt.Sprintf("  %saction %s: %s", prefix, event.Subject, event.Message))
		} else {
			v.emitLogLocked(fmt.Sprintf("  %saction %s is making progress", prefix, event.Subject))
		}
	case "completed":
		v.emitLogLocked(fmt.Sprintf("  %saction %s completed", prefix, event.Subject))
	default:
		v.emitLogLocked(fmt.Sprintf("  %saction %s %s", prefix, event.Subject, event.Status))
	}

	if v.liveMode {
		v.redrawLiveLocked()
	}
}

func (v *RunbookExecuteHuman) ExecutingStep(step *runbookruntime.Step) {
	if step == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	v.beginExecutionOutputLocked()
	state := v.syncStepState(step)
	v.events = append(v.events, runbookExecuteEvent{StepName: step.Name, StepIndex: step.Index, Status: runbookruntime.StepStatusRunning})
	if state == nil || state.TerminalReported || state.RunningReported {
		if v.liveMode {
			v.redrawLiveLocked()
		}
		return
	}

	state.RunningReported = true
	state.Status = runbookruntime.StepStatusRunning
	v.emitLogLocked(fmt.Sprintf("-> %s is in progress", v.renderStepLabel(state)))
	if v.liveMode {
		v.redrawLiveLocked()
	}
}

func (v *RunbookExecuteHuman) ExecutedStep(step *runbookruntime.Step) {
	if step == nil || step.Status == runbookruntime.StepStatusRunning || step.Status == runbookruntime.StepStatusPlanned || step.Status == runbookruntime.StepStatusPending {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	v.beginExecutionOutputLocked()
	state := v.findStepStateLocked(step)
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
		if v.liveMode {
			v.redrawLiveLocked()
		}
		return
	}
	if !state.RunningReported {
		state.RunningReported = true
		v.emitLogLocked(fmt.Sprintf("-> %s is in progress", v.renderStepLabel(state)))
	}

	state.TerminalReported = true
	switch step.Status {
	case runbookruntime.StepStatusCompleted:
		v.emitLogLocked(fmt.Sprintf("ok %s completed", v.renderStepLabel(state)))
	case runbookruntime.StepStatusSkipped:
		line := fmt.Sprintf("sk %s skipped", v.renderStepLabel(state))
		if step.SkipReason != "" {
			line = fmt.Sprintf("%s: %s", line, step.SkipReason)
		}
		v.emitLogLocked(line)
	case runbookruntime.StepStatusFailed:
		line := fmt.Sprintf("!! %s failed", v.renderStepLabel(state))
		if step.SkipReason != "" {
			line = fmt.Sprintf("%s: %s", line, step.SkipReason)
		}
		v.emitLogLocked(line)
	}

	if v.liveMode {
		v.redrawLiveLocked()
	}
}

func (v *RunbookExecuteHuman) Executed(plan *runbookgraph.Plan) {
	v.mu.Lock()
	v.stopSpinnerLoopLocked()
	if v.liveMode {
		v.redrawLiveLocked()
		v.renderedLines = 0
		v.view.streams.Print("\n")
	}
	v.mu.Unlock()

	completed := 0
	skipped := 0
	failed := 0
	for _, step := range plan.Steps {
		switch step.Status {
		case runbookruntime.StepStatusSkipped:
			skipped++
		case runbookruntime.StepStatusFailed:
			failed++
		default:
			completed++
		}
	}
	v.view.streams.Printf("Runbook execute complete. Steps: %d completed, %d skipped, %d failed.\n", completed, skipped, failed)

	outputs, outputDiags := collectRunbookExecuteOutputs(plan)
	v.Diagnostics(outputDiags)
	if len(outputs) == 0 {
		return
	}
	v.view.streams.Print(v.view.colorize.Color("[reset][bold][green]\nOutputs:\n\n"))
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

func (v *RunbookExecuteHuman) beginExecutionOutputLocked() {
	if v.started {
		return
	}
	v.started = true
	if v.liveMode {
		v.startSpinnerLoopLocked()
		v.redrawLiveLocked()
		return
	}
	v.view.streams.Println("Runbook execution started.")
}

func (v *RunbookExecuteHuman) syncStepState(step *runbookruntime.Step) *runbookExecuteStepState {
	if step == nil {
		return nil
	}
	addr := formatRunbookRuntimeStepAddress(step)
	state, ok := v.stepStateByAddr[addr]
	if !ok {
		state = &runbookExecuteStepState{
			Index:   step.Index,
			Order:   len(v.steps) + 1,
			Total:   len(v.steps) + 1,
			Address: addr,
		}
		if v.stepStateByAddr == nil {
			v.stepStateByAddr = make(map[string]*runbookExecuteStepState)
		}
		if v.stepStatesByIdx == nil {
			v.stepStatesByIdx = make(map[int][]*runbookExecuteStepState)
		}
		if v.stepStatesByName == nil {
			v.stepStatesByName = make(map[string][]*runbookExecuteStepState)
		}
		v.stepStateByAddr[addr] = state
		v.stepStatesByIdx[step.Index] = append(v.stepStatesByIdx[step.Index], state)
		v.stepStatesByName[step.Name] = append(v.stepStatesByName[step.Name], state)
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

func (v *RunbookExecuteHuman) findStepStateLocked(step *runbookruntime.Step) *runbookExecuteStepState {
	if step == nil {
		return nil
	}
	if state := v.stepStateByAddr[formatRunbookRuntimeStepAddress(step)]; state != nil {
		return state
	}
	return v.findStepStateForEventLocked(step.Name, step.Index)
}

func (v *RunbookExecuteHuman) findStepStateForEventLocked(stepName string, stepIndex int) *runbookExecuteStepState {
	if candidates := v.stepStatesByIdx[stepIndex]; len(candidates) == 1 {
		return candidates[0]
	}
	if candidates := v.stepStatesByIdx[stepIndex]; len(candidates) > 1 {
		for _, candidate := range candidates {
			if candidate != nil && candidate.Status == runbookruntime.StepStatusRunning {
				return candidate
			}
		}
		if stepName != "" {
			for _, candidate := range candidates {
				if candidate != nil && strings.HasPrefix(candidate.Address, "step."+stepName) {
					return candidate
				}
			}
		}
		return candidates[0]
	}
	if stepName == "" {
		return nil
	}
	candidates := v.stepStatesByName[stepName]
	if len(candidates) == 1 {
		return candidates[0]
	}
	for _, candidate := range candidates {
		if candidate != nil && candidate.Status == runbookruntime.StepStatusRunning {
			return candidate
		}
	}
	for _, candidate := range candidates {
		if candidate != nil && !candidate.TerminalReported {
			return candidate
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return nil
}

func (v *RunbookExecuteHuman) emitLogLocked(line string) {
	for _, sanitized := range sanitizeRunbookLogLines(line) {
		v.logs = append(v.logs, sanitized)
		if len(v.logs) > runbookExecuteLogRetention {
			v.logs = append([]string(nil), v.logs[len(v.logs)-runbookExecuteLogRetention:]...)
		}
		if !v.liveMode {
			v.view.streams.Println(sanitized)
		}
	}
}

func (v *RunbookExecuteHuman) startSpinnerLoopLocked() {
	if !v.liveMode || v.spinnerStop != nil {
		return
	}
	stop := make(chan struct{})
	v.spinnerStop = stop
	go func(stop <-chan struct{}) {
		ticker := time.NewTicker(runbookExecuteSpinnerEvery)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			v.mu.Lock()
			if v.spinnerStop != stop {
				v.mu.Unlock()
				return
			}
			v.spinnerIndex = (v.spinnerIndex + 1) % len(runbookExecuteSpinnerFrames)
			if v.hasRunningStepsLocked() {
				v.redrawLiveLocked()
			}
			v.mu.Unlock()
		}
	}(stop)
}

func (v *RunbookExecuteHuman) stopSpinnerLoopLocked() {
	if v.spinnerStop == nil {
		return
	}
	close(v.spinnerStop)
	v.spinnerStop = nil
}

func (v *RunbookExecuteHuman) hasRunningStepsLocked() bool {
	for _, step := range v.steps {
		if step != nil && step.Status == runbookruntime.StepStatusRunning {
			return true
		}
	}
	return false
}

func (v *RunbookExecuteHuman) redrawLiveLocked() {
	block := v.renderLiveBlockAtWidth(v.view.outputColumns())
	if v.renderedLines > 0 {
		v.view.streams.Printf("\x1b[%dA\r", v.renderedLines)
		v.view.streams.Print("\x1b[0m\x1b[J")
	} else {
		v.view.streams.Print("\x1b[0m")
	}
	v.view.streams.Print(block)
	v.renderedLines = strings.Count(block, "\n")
}

func (v *RunbookExecuteHuman) renderLiveBlockAtWidth(width int) string {
	if width < runbookExecuteMinimumWidth {
		width = runbookExecuteMinimumWidth
	}
	separator := strings.Repeat(runbookExecuteSectionDivider, width)
	lines := []runbookRenderLine{{Text: "Runbook execution", Green: true}, {Text: separator}}
	lines = append(lines, v.renderLiveLogsSection()...)
	lines = append(lines, runbookRenderLine{Text: separator})
	lines = append(lines, v.renderLiveInProgressSection()...)
	lines = append(lines, runbookRenderLine{Text: separator})
	lines = append(lines, v.renderLiveStatusSection()...)

	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		for _, part := range v.wrapLiveLines(line.Text, width) {
			if line.Green {
				wrapped = append(wrapped, v.greenLiveText(part))
			} else {
				wrapped = append(wrapped, part)
			}
		}
	}
	return strings.Join(wrapped, "\n") + "\n"
}

func (v *RunbookExecuteHuman) renderLiveLogsSection() []runbookRenderLine {
	ret := []runbookRenderLine{{Text: "Logs", Green: true}}
	if len(v.logs) == 0 {
		return append(ret, runbookRenderLine{Text: "  (waiting for output)"})
	}
	start := maxInt(0, len(v.logs)-runbookExecuteVisibleLogs)
	if start > 0 {
		ret = append(ret, runbookRenderLine{Text: fmt.Sprintf("  ... %d earlier line(s)", start)})
	}
	for _, line := range v.logs[start:] {
		ret = append(ret, runbookRenderLine{Text: "  " + line})
	}
	return ret
}

func (v *RunbookExecuteHuman) renderLiveInProgressSection() []runbookRenderLine {
	ret := []runbookRenderLine{{Text: "Steps in progress", Green: true}}
	running := 0
	frame := runbookExecuteSpinnerFrames[v.spinnerIndex%len(runbookExecuteSpinnerFrames)]
	for _, step := range v.steps {
		if step == nil || step.Status != runbookruntime.StepStatusRunning {
			continue
		}
		running++
		ret = append(ret, runbookRenderLine{Text: fmt.Sprintf("  %s %s", frame, v.renderStepLabel(step)), Green: true})
	}
	if running == 0 {
		ret = append(ret, runbookRenderLine{Text: "  (no steps currently in progress)", Green: true})
	}
	return ret
}

func (v *RunbookExecuteHuman) renderLiveStatusSection() []runbookRenderLine {
	statusParts := make([]string, 0, len(v.steps))
	completed := 0
	running := 0
	waiting := 0
	skipped := 0
	failed := 0
	for _, step := range v.steps {
		if step == nil {
			continue
		}
		statusParts = append(statusParts, fmt.Sprintf("[%d/%d %s=%s]", step.Order, step.Total, step.Address, liveStatusToken(step)))
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
	statusLine := "Status: (no steps)"
	if len(statusParts) > 0 {
		statusLine = "Status: " + strings.Join(statusParts, " ")
	}
	summaryLine := fmt.Sprintf("Summary: %d done, %d in progress, %d waiting, %d skipped, %d failed.", completed, running, waiting, skipped, failed)
	return []runbookRenderLine{{Text: statusLine, Green: true}, {Text: summaryLine, Green: true}}
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

func (v *RunbookExecuteHuman) greenLiveText(text string) string {
	if text == "" {
		return text
	}
	return v.view.colorize.Color("[reset][bold][green]" + text + "[reset]")
}

func sanitizeRunbookLogLines(line string) []string {
	if line == "" {
		return nil
	}
	normalized := strings.ReplaceAll(line, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	parts := strings.Split(normalized, "\n")
	ret := make([]string, 0, len(parts))
	for _, part := range parts {
		part = runbookANSIEscapeRE.ReplaceAllString(part, "")
		ret = append(ret, strings.TrimRight(part, "\t "))
	}
	for len(ret) > 0 && ret[len(ret)-1] == "" {
		ret = ret[:len(ret)-1]
	}
	return ret
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

func liveStatusToken(step *runbookExecuteStepState) string {
	if step == nil {
		return "unknown"
	}
	switch step.Status {
	case runbookruntime.StepStatusCompleted:
		return "done"
	case runbookruntime.StepStatusSkipped:
		return "skipped"
	case runbookruntime.StepStatusFailed:
		return "failed"
	case runbookruntime.StepStatusRunning:
		return "in-progress"
	default:
		return "waiting"
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (v *RunbookExecuteHuman) renderActionPrefix(stepIndex int) string {
	state := v.findStepStateForEventLocked("", stepIndex)
	if state == nil {
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

func formatRunbookRuntimeStepAddress(step *runbookruntime.Step) string {
	if step == nil {
		return "step"
	}
	if step.InstanceKey == nil {
		return fmt.Sprintf("step.%s", step.Name)
	}
	return fmt.Sprintf("step.%s%s", step.Name, step.InstanceKey.String())
}

func collectRunbookExecuteOutputs(plan *runbookgraph.Plan) (map[string]cty.Value, tfdiags.Diagnostics) {
	if plan == nil {
		return nil, nil
	}
	return plan.OutputValues()
}
