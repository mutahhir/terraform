package command

import (
	"os"
	"strings"

	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/command/views"
	"github.com/hashicorp/terraform/internal/depsfile"
	"github.com/hashicorp/terraform/internal/providercache"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
)

type RunbookInitCommand struct {
	runbookCommandBase
}

func NewRunbookInitCommand(meta Meta) *RunbookInitCommand {
	return &RunbookInitCommand{runbookCommandBase: runbookCommandBase{Meta: meta}}
}

func (c *RunbookInitCommand) Run(rawArgs []string) int {
	common, rawArgs := arguments.ParseView(rawArgs)
	c.View.Configure(common)
	c.Meta.color = !common.NoColor
	c.Meta.Color = c.Meta.color

	args, diags := arguments.ParseRunbookInit(rawArgs)
	view := views.NewInit(args.ViewType, c.View)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}

	pwd, err := os.Getwd()
	if err != nil {
		view.Diagnostics(diags.Append(err))
		return 1
	}

	runbookDir, workspaceDir, pathDiags := c.discoverRunbookPaths(pwd)
	diags = diags.Append(pathDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}

	// Set up the view to read from the runbook config sources, so that
	// any diagnostics emitted from parsing the runbook configs will have the appropriate context.
	c.View.SetConfigSources(func() map[string][]byte {
		return runbookConfigSources(runbookDir)
	})

	parser := runbookconfigs.NewRunbookParser(nil)
	config, parseDiags := loadRunbookConfigWithWorkspace(parser, runbookDir, workspaceDir)
	diags = diags.Append(parseDiags)
	diags = diags.Append(validateDeclaredRunbookProviderUsage(config))
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}

	reqs, reqDiags := runbookProviderRequirements(config)
	diags = diags.Append(reqDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}
	workspaceReqs := runbookWorkspaceProviderRequirements(config)
	for provider, constraints := range workspaceReqs {
		if _, exists := reqs[provider]; !exists {
			reqs[provider] = constraints
		}
	}

	if err := os.MkdirAll(runbookDataDirPath(runbookDir), 0o755); err != nil {
		view.Diagnostics(diags.Append(err))
		return 1
	}

	ctx, done := c.InterruptibleContext(c.CommandContext())
	defer done()

	meta := c.runbookMeta(runbookDir)
	inst := meta.providerInstaller()
	previousLocks, lockDiags := c.runbookLockedDependencies(runbookDir)
	diags = diags.Append(lockDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}
	workspaceLocks, workspaceLockDiags := workspaceLockedDependencies(workspaceDir)
	diags = diags.Append(workspaceLockDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}
	startingLocks := c.mergeLockedDependencies(previousLocks, workspaceLocks)

	mode := providercache.InstallNewProvidersOnly
	if args.Upgrade {
		mode = providercache.InstallUpgrades
	}

	view.Log("Initializing runbook providers...")
	newLocks, err := inst.EnsureProviderVersions(ctx, startingLocks, reqs, mode)
	if err != nil {
		view.Diagnostics(diags.Append(err))
		return 1
	}
	if newLocks == nil {
		newLocks = depsfile.NewLocks()
	}

	lockFileDiags := c.replaceRunbookLockedDependencies(runbookDir, newLocks)
	diags = diags.Append(lockFileDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}
	metadataDiags := writeRunbookDependencyMetadata(runbookDir, generateRunbookDependencyMetadata(config, workspaceLocks))
	diags = diags.Append(metadataDiags)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}

	view.Log("Writing lock file: %s", runbookDependencyLockFilename)
	view.Log("Installing providers into: %s/", runbookDataDirName)
	view.Log("Runbook initialization complete.")
	return 0
}

func (c *RunbookInitCommand) Help() string {
	return strings.TrimSpace(`
Usage: terraform [global options] runbook init [options]

  Initializes the runbook in the current runbook directory.

Options:

  -json             Emit machine-readable JSON output.
  -upgrade          Upgrade provider selections recorded in the runbook lock file.
  -no-color         Disable color in output.
`)
}

func (c *RunbookInitCommand) Synopsis() string {
	return "Prepare a runbook working directory"
}
