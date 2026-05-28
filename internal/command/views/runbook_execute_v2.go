package views

import (
	"fmt"
	"strings"
	"time"

	runbookgraph "github.com/hashicorp/terraform/internal/runbooks/graph"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
)

// renderLiveBlockV2 produces the in-place-redrawn block for live TTY mode.
// Top: execution graph with animated dots.
// Bottom: event stream with action progress.
func (v *RunbookExecuteHuman) renderLiveBlockV2(width int) string {
	if width < runbookExecuteMinimumWidth {
		width = runbookExecuteMinimumWidth
	}
	sym := selectExecSymbols(v.view)
	c := v.view.colorize
	pad := planLeftPad

	var b strings.Builder

	// Header
	b.WriteString(c.Color(pad + sym.ColorHeader + "Runbook execute" + sym.ColorReset + "\n"))
	b.WriteString("\n")

	// Graph section — one line per step with state dot
	for _, step := range v.steps {
		if step == nil {
			continue
		}
		b.WriteString(v.renderExecGraphLine(step, sym, pad))
	}
	b.WriteString("\n")

	// Separator
	sep := pad + strings.Repeat("─", minInt(width-len(pad), 57))
	if sym.DotDone == "*" {
		sep = pad + strings.Repeat("-", minInt(width-len(pad), 57))
	}
	b.WriteString(c.Color(sym.ColorDim + sep + sym.ColorReset + "\n"))

	// Event stream — last N lines
	visible := v.visibleEventLines()
	for _, line := range visible {
		b.WriteString(pad + line + "\n")
	}
	if len(visible) == 0 {
		b.WriteString(c.Color(pad + sym.ColorDim + "(waiting for output)" + sym.ColorReset + "\n"))
	}

	return b.String()
}

func (v *RunbookExecuteHuman) renderExecGraphLine(step *runbookExecuteStepState, sym runbookExecSymbols, pad string) string {
	c := v.view.colorize
	addr := step.Address

	switch step.Status {
	case runbookruntime.StepStatusRunning:
		frame := sym.SpinnerFrames[v.spinnerIndex%len(sym.SpinnerFrames)]
		dur := v.formatStepDuration(step)
		return c.Color(fmt.Sprintf("%s%s%s %s%s %s %s%s\n", pad, sym.ColorRunning, sym.DotRunning, addr, sym.ColorReset, frame, dur, sym.ColorReset))

	case runbookruntime.StepStatusCompleted:
		dur := v.formatStepDuration(step)
		return c.Color(fmt.Sprintf("%s%s%s %s%s %s\n", pad, sym.ColorDone, sym.DotDone, addr, sym.ColorReset, dur))

	case runbookruntime.StepStatusSkipped:
		return c.Color(fmt.Sprintf("%s%s%s %s skipped%s\n", pad, sym.ColorSkip, sym.DotSkip, addr, sym.ColorReset))

	case runbookruntime.StepStatusFailed:
		return c.Color(fmt.Sprintf("%s%s%s %s FAILED%s\n", pad, sym.ColorFailed, sym.DotFailed, addr, sym.ColorReset))

	default: // waiting/planned
		return c.Color(fmt.Sprintf("%s%s%s %s%s\n", pad, sym.ColorWaiting, sym.DotWaiting, addr, sym.ColorReset))
	}
}

func (v *RunbookExecuteHuman) formatStepDuration(step *runbookExecuteStepState) string {
	if step == nil || step.StartedAt.IsZero() {
		return ""
	}
	end := step.FinishedAt
	if end.IsZero() {
		end = time.Now()
	}
	d := end.Sub(step.StartedAt).Round(time.Second)
	if d < time.Second {
		return "<1s"
	}
	return d.String()
}

// visibleEventLines returns the last N event log lines for display.
const execVisibleEventLines = 12

func (v *RunbookExecuteHuman) visibleEventLines() []string {
	if len(v.logs) == 0 {
		return nil
	}
	start := len(v.logs) - execVisibleEventLines
	if start < 0 {
		start = 0
	}
	ret := make([]string, 0, execVisibleEventLines)
	if start > 0 {
		ret = append(ret, fmt.Sprintf("... (%d earlier lines)", start))
	}
	ret = append(ret, v.logs[start:]...)
	return ret
}

// renderNonTTYStepStart emits the step-starting line for non-TTY mode.
func (v *RunbookExecuteHuman) renderNonTTYStepStart(step *runbookExecuteStepState) string {
	sym := selectExecSymbols(v.view)
	return fmt.Sprintf("%s%s %s: executing...", planLeftPad, sym.DotDone, step.Address)
}

// renderNonTTYStepEnd emits the step-completion line for non-TTY mode.
func (v *RunbookExecuteHuman) renderNonTTYStepEnd(step *runbookExecuteStepState) string {
	sym := selectExecSymbols(v.view)
	dur := v.formatStepDuration(step)
	switch step.Status {
	case runbookruntime.StepStatusCompleted:
		return fmt.Sprintf("%s%s %s: completed (%s)", planLeftPad, sym.DotDone, step.Address, dur)
	case runbookruntime.StepStatusSkipped:
		line := fmt.Sprintf("%s%s %s: skipped", planLeftPad, sym.DotSkip, step.Address)
		if step.SkipReason != "" {
			line += "\n" + planLeftPad + "  reason: " + step.SkipReason
		}
		return line
	case runbookruntime.StepStatusFailed:
		line := fmt.Sprintf("%s%s %s: FAILED", planLeftPad, sym.DotFailed, step.Address)
		if step.SkipReason != "" {
			line += "\n" + planLeftPad + "  error: " + step.SkipReason
		}
		return line
	default:
		return fmt.Sprintf("%s%s %s: %s", planLeftPad, sym.DotDone, step.Address, step.Status)
	}
}

// formatActionEventV2 formats an action event for the event stream.
func formatActionEventV2(event runbookgraph.ActionExecEvent, sym runbookExecSymbols, c interface{ Color(string) string }) string {
	pad := planLeftPad

	// Wait events get distinct formatting
	if event.ActionType == "wait" {
		switch event.Status {
		case "waiting":
			return c.Color(fmt.Sprintf("%s  %s%s%s %s — %s", pad, sym.ColorCondition, sym.Condition, sym.ColorReset, event.Subject, event.Message))
		case "progress":
			if event.Message != "" {
				return c.Color(fmt.Sprintf("%s     %s%s%s", pad, sym.ColorDim, event.Message, sym.ColorReset))
			}
			return ""
		case "satisfied":
			return c.Color(fmt.Sprintf("%s     %s%s ✓%s", pad, sym.ColorDone, event.Message, sym.ColorReset))
		case "timed_out":
			return c.Color(fmt.Sprintf("%s     %s%s%s", pad, sym.ColorFailed, event.Message, sym.ColorReset))
		case "completed":
			return c.Color(fmt.Sprintf("%s     %s%s%s", pad, sym.ColorDim, event.Message, sym.ColorReset))
		default:
			return c.Color(fmt.Sprintf("%s  %s%s%s %s: %s", pad, sym.ColorCondition, sym.Condition, sym.ColorReset, event.Subject, event.Message))
		}
	}

	switch event.Status {
	case "running":
		return c.Color(fmt.Sprintf("%s  %s%s%s %s", pad, sym.ColorAction, sym.Action, sym.ColorReset, event.Subject))
	case "progress":
		if event.Message != "" {
			return c.Color(fmt.Sprintf("%s     %s%s%s", pad, sym.ColorDim, event.Message, sym.ColorReset))
		}
		return ""
	case "completed":
		return c.Color(fmt.Sprintf("%s     %scompleted%s", pad, sym.ColorDim, sym.ColorReset))
	default:
		return c.Color(fmt.Sprintf("%s  %s%s%s %s: %s", pad, sym.ColorAction, sym.Action, sym.ColorReset, event.Subject, event.Status))
	}
}

// renderExecSummary produces the final summary line.
func renderExecSummary(steps []*runbookExecuteStepState, totalDuration time.Duration) string {
	completed, skipped, failed := 0, 0, 0
	for _, step := range steps {
		if step == nil {
			continue
		}
		switch step.Status {
		case runbookruntime.StepStatusCompleted:
			completed++
		case runbookruntime.StepStatusSkipped:
			skipped++
		case runbookruntime.StepStatusFailed:
			failed++
		}
	}

	parts := []string{}
	if completed > 0 {
		parts = append(parts, fmt.Sprintf("%d completed", completed))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}

	dur := totalDuration.Round(time.Second).String()
	return fmt.Sprintf("%sExecute complete: %s (%s total)", planLeftPad, strings.Join(parts, ", "), dur)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
