package command

import (
	"strings"

	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/command/views"
	runbookgraph "github.com/hashicorp/terraform/internal/runbooks/graph"
)

type RunbookPlanCommand struct {
	runbookCommandBase
}

func NewRunbookPlanCommand(meta Meta) *RunbookPlanCommand {
	return &RunbookPlanCommand{runbookCommandBase: runbookCommandBase{Meta: meta}}
}

func (c *RunbookPlanCommand) Run(rawArgs []string) int {
	common, rawArgs := arguments.ParseView(rawArgs)
	c.View.Configure(common)
	c.Meta.color = !common.NoColor
	c.Meta.Color = c.Meta.color

	args, diags := arguments.ParseRunbookPlan(rawArgs)
	view := views.NewRunbookPlan(args.ViewType, c.View)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		view.HelpPrompt()
		return 1
	}

	loaded, loadDiags := c.loadRunbook(rawArgs, args.Vars)
	diags = diags.Append(loadDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}

	inputValues, valueDiags := c.collectRunbookVariableValues(loaded.Config, args.Vars)
	diags = diags.Append(valueDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}

	plan, planDiags := runbookgraph.BuildPlan(loaded.Config, &runbookgraph.PlannerOpts{
		InputValues:    inputValues,
		Providers:      loaded.ProviderFactories,
		WorkspaceState: loaded.WorkspaceState,
		UI:             view.UI(),
		Hooks:          view.Hooks(),
	})
	diags = diags.Append(planDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}

	view.Plan(plan)
	return 0
}

func (c *RunbookPlanCommand) Help() string {
	return strings.TrimSpace(`
Usage: terraform [global options] runbook plan [options]

  Builds a speculative plan for the runbook in the current runbook directory.

Options:

  -json             Emit machine-readable JSON output.
  -var 'foo=bar'    Set a value for one of the runbook input variables.
  -var-file=FILE    Load variable values from the given file.
  -no-color         Disable color in output.
`)
}

func (c *RunbookPlanCommand) Synopsis() string {
	return "Show the planned runbook steps"
}
