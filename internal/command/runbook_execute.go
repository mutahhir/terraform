package command

import (
	"strings"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/command/views"
	"github.com/hashicorp/terraform/internal/providers"
	runbookgraph "github.com/hashicorp/terraform/internal/runbooks/graph"
	"github.com/hashicorp/terraform/internal/states"
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

	var plan *runbookgraph.Plan
	var inputValues terraform.InputValues
	var providerFactories map[terraformaddrs.Provider]providers.Factory
	var workspaceState *states.State
	if args.PlanPath != "" {
		saved, config, savedWorkspaceState, factories, loadDiags := c.loadSavedRunbookPlan(args.PlanPath)
		diags = diags.Append(loadDiags)
		if diags.HasErrors() {
			view.Diagnostics(diags)
			return 1
		}
		providerFactories = factories
		workspaceState = savedWorkspaceState
		plan, diags = runbookgraph.ImportSavedPlan(config, saved, savedWorkspaceState, factories)
		if diags.HasErrors() {
			view.Diagnostics(diags)
			return 1
		}
	} else {
		loaded, loadDiags := c.loadRunbook(rawArgs, args.Vars)
		diags = diags.Append(loadDiags)
		if diags.HasErrors() {
			view.Diagnostics(diags)
			return 1
		}

		var valueDiags tfdiags.Diagnostics
		inputValues, valueDiags = c.collectRunbookVariableValues(loaded.Config, args.Vars)
		diags = diags.Append(valueDiags)
		if diags.HasErrors() {
			view.Diagnostics(diags)
			return 1
		}
		providerFactories = loaded.ProviderFactories
		workspaceState = loaded.WorkspaceState
		plan, diags = runbookgraph.BuildPlan(loaded.Config, &runbookgraph.PlannerOpts{
			InputValues:    inputValues,
			Providers:      loaded.ProviderFactories,
			WorkspaceState: loaded.WorkspaceState,
			UI:             planView.UI(),
			Hooks:          planView.Hooks(),
		})
		if diags.HasErrors() {
			view.Diagnostics(diags)
			return 1
		}
	}
	view.Prepare(plan)
	planView.Plan(plan)
	// Both producers (BuildPlan / ImportSavedPlan) spin up live provider plugin
	// instances at plan/import time. Ensure they are torn down on every exit
	// path, including the confirmation-decline and other early returns below
	// where ExecutePlan (which also closes the pool) never runs. Plan.Close is
	// idempotent, so the later ExecutePlan teardown is a harmless no-op.
	defer plan.Close()

	if args.PlanPath == "" && !args.AutoApprove && args.ViewType != arguments.ViewJSON {
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
		Providers:      providerFactories,
		WorkspaceState: workspaceState,
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
Usage: terraform [global options] runbook execute [options] [PLANFILE]

  Executes the runbook in the current runbook directory, or from a saved
  runbook plan file.

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
