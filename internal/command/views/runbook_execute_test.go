package views

import (
	"strings"
	"testing"
	"time"

	runbookgraph "github.com/hashicorp/terraform/internal/runbooks/graph"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terminal"
)

func TestRunbookExecuteHumanRenderLiveBlockAtWidth(t *testing.T) {
	streams, done := terminal.StreamsForTesting(t)
	defer done(t)

	view := NewView(streams)
	human := &RunbookExecuteHuman{view: view}
	human.Prepare(&runbookgraph.Plan{Steps: []*runbookruntime.Step{
		{Name: "discover", Index: 0, Status: runbookruntime.StepStatusPlanned},
		{Name: "deploy", Index: 1, Status: runbookruntime.StepStatusRunning},
		{Name: "cleanup", Index: 2, Status: runbookruntime.StepStatusSkipped, SkipReason: "not required"},
	}})

	human.liveMode = true
	human.spinnerIndex = 2
	human.logs = []string{
		"-> [1/3] step.discover is in progress",
		"ok [1/3] step.discover completed",
		"-> [2/3] step.deploy is in progress",
		"  [2/3] action action.test_action.notify: invoking",
	}
	human.stepStateByAddr["step.discover"].Status = runbookruntime.StepStatusCompleted
	human.stepStateByAddr["step.deploy"].Status = runbookruntime.StepStatusRunning
	human.stepStateByAddr["step.cleanup"].Status = runbookruntime.StepStatusSkipped

	block := human.renderLiveBlockAtWidth(60)
	if !strings.Contains(block, "Logs") {
		t.Fatalf("expected logs section, got: %s", block)
	}
	if !strings.Contains(block, "Steps in progress") {
		t.Fatalf("expected in-progress section, got: %s", block)
	}
	if !strings.Contains(block, "Status") {
		t.Fatalf("expected status section, got: %s", block)
	}
	if !strings.Contains(block, "[2/3] step.deploy = in-progress") {
		t.Fatalf("expected running step on its own status line, got: %s", block)
	}
	for _, line := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
		if got := len([]rune(line)); got > 60 {
			t.Fatalf("expected width-aware line wrapping, got line %q with width %d", line, got)
		}
	}
}

func TestRunbookExecuteHumanRenderExecutionReport(t *testing.T) {
	streams, done := terminal.StreamsForTesting(t)
	view := NewView(streams)
	human := &RunbookExecuteHuman{view: view}
	human.Prepare(&runbookgraph.Plan{Steps: []*runbookruntime.Step{{Name: "discover", Index: 0, Status: runbookruntime.StepStatusCompleted}}})

	step := human.stepStateByAddr["step.discover"]
	step.Status = runbookruntime.StepStatusCompleted
	step.StartedAt = time.Unix(0, 0)
	step.FinishedAt = step.StartedAt.Add(1500 * time.Millisecond)
	step.LogLines = []string{
		"",
		"-> [1/1] step.discover is in progress",
		"  [1/1] action action.test.notify: invoking",
		"",
		"",
		"ok [1/1] step.discover completed",
	}

	human.renderExecutionReport(nil)
	output := done(t).Stdout()
	if !strings.Contains(output, "Execution Report") {
		t.Fatalf("expected execution report header, got: %s", output)
	}
	if !strings.Contains(output, "Step 1: step.discover [Status: Complete] [Duration: 1.5s]") {
		t.Fatalf("expected step entry with duration, got: %s", output)
	}
	if !strings.Contains(output, "| step: [1/1] step.discover is in progress") {
		t.Fatalf("expected normalized step log, got: %s", output)
	}
	if !strings.Contains(output, "|   action: [1/1] action action.test.notify: invoking") {
		t.Fatalf("expected nested action log, got: %s", output)
	}
	if strings.Contains(output, "|\n    |\n") {
		t.Fatalf("expected blank lines to be collapsed, got: %s", output)
	}
}
