package command

import (
	"strings"

	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/command/views"
	runbookgraph "github.com/hashicorp/terraform/internal/runbooks/graph"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type RunbookExecuteCommand struct {
	runbookCommandBase
}

func NewRunbookExecuteCommand(meta Meta) *RunbookExecuteCommand {
	return &RunbookExecuteCommand{runbookCommandBase: runbookCommandBase{Meta: meta}}
}

func (c *RunbookExecuteCommand) Run(rawArgs []string) int {
	common, rawArgs := arguments.ParseView(rawArgs)
	c.View.Configure(common)
	c.Meta.color = !common.NoColor
	c.Meta.Color = c.Meta.color

	args, diags := arguments.ParseRunbookExecute(rawArgs)
	view := views.NewRunbookExecute(args.ViewType, c.View)
	planView := views.NewRunbookPlan(args.ViewType, c.View)
	c.Meta.input = args.InputEnabled
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
		UI:             planView.UI(),
		Hooks:          planView.Hooks(),
	})
	diags = diags.Append(planDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}
	view.Prepare(plan)
	planView.Plan(plan)

	if !args.AutoApprove && args.ViewType != arguments.ViewJSON {
		c.Ui.Output("Runbook actions will be executed. Only 'yes' will be accepted to continue.\n")
		confirmed, err := c.confirm(&terraform.InputOpts{Id: "runbook-execute-approve", Query: "Do you want to perform these runbook steps?"})
		if err != nil {
			view.Diagnostics(tfdiags.Diagnostics{}.Append(err))
			return 1
		}
		if !confirmed {
			view.Diagnostics(tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(tfdiags.Error, "Runbook execution cancelled", "User declined to execute the planned runbook steps.")))
			return 1
		}
	}

	execDiags := runbookgraph.ExecutePlan(plan, &runbookgraph.ExecuteOpts{
		InputValues:    inputValues,
		Providers:      loaded.ProviderFactories,
		WorkspaceState: loaded.WorkspaceState,
		UI:             view.UI(),
		Hooks:          view.Hooks(),
	})
	if execDiags.HasErrors() {
		view.Diagnostics(execDiags)
		return 1
	}
	view.Executed(plan)
	return 0
}

func (c *RunbookExecuteCommand) Help() string {
	return strings.TrimSpace(`
Usage: terraform [global options] runbook execute [options]

  Executes the runbook in the current runbook directory.

Options:

  -json             Emit machine-readable JSON output.
  -auto-approve     Skip interactive approval before execution.
  -var 'foo=bar'    Set a value for one of the runbook input variables.
  -var-file=FILE    Load variable values from the given file.
  -no-color         Disable color in output.
`)
}

func (c *RunbookExecuteCommand) Synopsis() string {
	return "Execute the planned runbook steps"
}
