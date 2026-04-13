package command

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform/internal/backend/backendrun"
	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/command/views"
	"github.com/hashicorp/terraform/internal/configs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runbookgraph "github.com/hashicorp/terraform/internal/runbooks/graph"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type RunbookPlanCommand struct {
	Meta
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

	var err error
	if c.pluginPath, err = c.loadPluginPath(); err != nil {
		view.Diagnostics(diags.Append(err))
		return 1
	}

	pwd, err := os.Getwd()
	if err != nil {
		view.Diagnostics(tfdiags.Diagnostics{}.Append(err))
		return 1
	}

	runbookDir, workspaceDir, pathDiags := c.discoverRunbookPaths(pwd)
	diags = diags.Append(pathDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}

	parser := runbookconfigs.NewRunbookParser(nil)
	config, parseDiags := parser.LoadRunbookConfigDir(runbookDir, workspaceDir)
	diags = diags.Append(parseDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}

	providerFactories, err := c.ProviderFactories()
	if err != nil {
		view.Diagnostics(diags.Append(err))
		return 1
	}

	inputValues, valueDiags := c.collectRunbookVariableValues(config, args)
	diags = diags.Append(valueDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}

	plan, planDiags := runbookgraph.BuildPlan(config, &runbookgraph.PlannerOpts{
		InputValues: inputValues,
		Providers:   providerFactories,
		UI:          view.UI(),
		Hooks:       view.Hooks(),
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

  Builds a speculative plan for the runbook in the current working directory.

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

func (c *RunbookPlanCommand) discoverRunbookPaths(pwd string) (string, string, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	entries, err := os.ReadDir(pwd)
	if err != nil {
		return "", "", diags.Append(err)
	}
	hasRunbook := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".tfrun.hcl") {
			hasRunbook = true
			break
		}
	}
	if !hasRunbook {
		return "", "", diags.Append(tfdiags.Sourceless(tfdiags.Error, "No runbook configuration files", "Runbook plan requires at least one .tfrun.hcl file in the current working directory."))
	}

	return pwd, discoverRunbookWorkspaceDir(pwd), diags
}

func discoverRunbookWorkspaceDir(runbookDir string) string {
	parser := configs.NewParser(nil)
	for dir := runbookDir; ; dir = filepath.Dir(dir) {
		if parser.IsConfigDir(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	return runbookDir
}

func (c *RunbookPlanCommand) collectRunbookVariableValues(config *runbookconfigs.RunbookConfig, args *arguments.RunbookPlan) (terraform.InputValues, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if config == nil {
		return nil, nil
	}

	values, valueDiags := args.Vars.CollectValues(func(filename string, src []byte) {})
	diags = diags.Append(valueDiags)
	declared, declaredDiags := backendrun.ParseVariableValues(values, config.Variables)
	diags = diags.Append(declaredDiags)
	return declared, diags
}
