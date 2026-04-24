package command

import (
	"strings"

	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/command/views"
	runbookgraph "github.com/hashicorp/terraform/internal/runbooks/graph"
)

type RunbookShowCommand struct {
	runbookCommandBase
}

func NewRunbookShowCommand(meta Meta) *RunbookShowCommand {
	return &RunbookShowCommand{runbookCommandBase: runbookCommandBase{Meta: meta}}
}

func (c *RunbookShowCommand) Run(rawArgs []string) int {
	common, rawArgs := arguments.ParseView(rawArgs)
	c.View.Configure(common)
	c.Meta.color = !common.NoColor
	c.Meta.Color = c.Meta.color

	args, diags := arguments.ParseRunbookShow(rawArgs)
	view := views.NewRunbookShow(args.ViewType, c.View)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		if args.ViewType == arguments.ViewHuman {
			c.View.HelpPrompt("runbook show")
		}
		return 1
	}

	saved, config, workspaceState, loadDiags := c.loadSavedRunbookPlanBundle(args.PlanPath)
	diags = diags.Append(loadDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}

	plan, diags := runbookgraph.ImportSavedPlan(config, saved, workspaceState, nil)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}

	view.Plan(plan)
	return 0
}

func (c *RunbookShowCommand) Help() string {
	return strings.TrimSpace(`
Usage: terraform [global options] runbook show [options] PLANFILE

  Shows a saved runbook plan in a human-readable or JSON format.

Options:

  -json             Emit machine-readable JSON output.
  -no-color         Disable color in output.
`)
}

func (c *RunbookShowCommand) Synopsis() string {
	return "Show a saved runbook plan"
}
