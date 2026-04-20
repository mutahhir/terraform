package views

import (
	"fmt"

	"github.com/hashicorp/terraform/internal/command/arguments"
	runbookgraph "github.com/hashicorp/terraform/internal/runbooks/graph"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type RunbookExecute interface {
	Diagnostics(tfdiags.Diagnostics)
	HelpPrompt()
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
	view *View
}

func (v *RunbookExecuteHuman) Diagnostics(diags tfdiags.Diagnostics) { v.view.Diagnostics(diags) }
func (v *RunbookExecuteHuman) HelpPrompt()                           { v.view.HelpPrompt("runbook execute") }
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
}

type RunbookExecuteJSON struct {
	view *JSONView
}

func (v *RunbookExecuteJSON) Diagnostics(diags tfdiags.Diagnostics) { v.view.Diagnostics(diags) }
func (v *RunbookExecuteJSON) HelpPrompt()                           {}
func (v *RunbookExecuteJSON) Executed(plan *runbookgraph.Plan) {
	v.view.log.Info("Runbook execute", "type", "runbook_execute", "plan", buildRunbookPlan(plan, nil))
}
