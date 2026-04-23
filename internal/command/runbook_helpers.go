package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/backend/backendrun"
	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/command/workdir"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/depsfile"
	"github.com/hashicorp/terraform/internal/getproviders/providerreqs"
	"github.com/hashicorp/terraform/internal/providers"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/states"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

const (
	runbookDependencyLockFilename = ".tfrun.lock.hcl"
	runbookDataDirName            = ".tfrun"
)

type runbookCommandBase struct {
	Meta
}

type loadedRunbook struct {
	RunbookDir        string
	WorkspaceDir      string
	Config            *runbookconfigs.RunbookConfig
	ProviderFactories map[terraformaddrs.Provider]providers.Factory
	WorkspaceState    *states.State
}

func (c *runbookCommandBase) loadRunbook(rawArgs []string, vars *arguments.Vars) (*loadedRunbook, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	var err error
	if c.pluginPath, err = c.loadPluginPath(); err != nil {
		return nil, diags.Append(err)
	}

	pwd, err := os.Getwd()
	if err != nil {
		return nil, diags.Append(err)
	}

	runbookDir, workspaceDir, pathDiags := c.discoverRunbookPaths(pwd)
	diags = diags.Append(pathDiags)
	if diags.HasErrors() {
		return nil, diags
	}
	c.View.SetConfigSources(func() map[string][]byte {
		return runbookConfigSources(runbookDir)
	})

	parser := runbookconfigs.NewRunbookParser(nil)
	config, parseDiags := parser.LoadRunbookConfigDir(runbookDir, workspaceDir)
	diags = diags.Append(parseDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	providerFactories, err := c.runbookProviderFactories(runbookDir)
	if err != nil {
		return nil, diags.Append(err)
	}

	b, backendDiags := c.backend(workspaceDir, arguments.ViewHuman)
	diags = diags.Append(backendDiags)
	if diags.HasErrors() {
		return nil, diags
	}
	c.ignoreRemoteVersionConflict(b)

	workspaceName, err := c.Workspace()
	if err != nil {
		return nil, diags.Append(err)
	}
	stateFile, err := getStateFromBackend(b, workspaceName)
	if err != nil {
		return nil, diags.Append(err)
	}
	var workspaceState *states.State
	if stateFile != nil {
		workspaceState = stateFile.State
	}

	return &loadedRunbook{
		RunbookDir:        runbookDir,
		WorkspaceDir:      workspaceDir,
		Config:            config,
		ProviderFactories: providerFactories,
		WorkspaceState:    workspaceState,
	}, diags
}

func runbookConfigSources(runbookDir string) map[string][]byte {
	ret := map[string][]byte{}
	entries, err := os.ReadDir(runbookDir)
	if err != nil {
		return ret
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tfrun.hcl") {
			continue
		}
		path := filepath.Join(runbookDir, entry.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		ret[path] = src
	}
	return ret
}

func runbookDependencyLockPath(runbookDir string) string {
	return filepath.Join(runbookDir, runbookDependencyLockFilename)
}

func runbookDataDirPath(runbookDir string) string {
	return filepath.Join(runbookDir, runbookDataDirName)
}

func (c *runbookCommandBase) runbookMeta(runbookDir string) Meta {
	meta := c.Meta
	wd := workdir.NewDir(runbookDir)
	if c.WorkingDir != nil {
		wd.OverrideOriginalWorkingDir(c.WorkingDir.OriginalWorkingDir())
	}
	wd.OverrideDataDir(runbookDataDirPath(runbookDir))
	meta.WorkingDir = wd
	return meta
}

func (c *runbookCommandBase) runbookLockedDependencies(runbookDir string) (*depsfile.Locks, tfdiags.Diagnostics) {
	path := runbookDependencyLockPath(runbookDir)
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return c.annotateDependencyLocksWithOverrides(depsfile.NewLocks()), nil
	}
	if err != nil {
		return nil, tfdiags.Diagnostics{}.Append(err)
	}

	ret, diags := depsfile.LoadLocksFromFile(path)
	return c.annotateDependencyLocksWithOverrides(ret), diags
}

func (c *runbookCommandBase) runbookProviderFactories(runbookDir string) (map[terraformaddrs.Provider]providers.Factory, error) {
	locks, diags := c.runbookLockedDependencies(runbookDir)
	if diags.HasErrors() {
		return nil, fmt.Errorf("failed to read runbook dependency lock file: %s", diags.Err())
	}
	meta := c.runbookMeta(runbookDir)
	return meta.ProviderFactoriesFromLocks(locks)
}

func (c *runbookCommandBase) replaceRunbookLockedDependencies(runbookDir string, new *depsfile.Locks) tfdiags.Diagnostics {
	return depsfile.SaveLocksToFile(new, runbookDependencyLockPath(runbookDir))
}

func runbookProviderRequirements(config *runbookconfigs.RunbookConfig) (providerreqs.Requirements, tfdiags.Diagnostics) {
	reqs := make(providerreqs.Requirements)
	if config == nil || config.ProviderRequirements == nil {
		return reqs, nil
	}

	var diags tfdiags.Diagnostics
	for _, providerReq := range config.ProviderRequirements.RequiredProviders {
		if providerReq == nil {
			continue
		}
		constraintStr := providerReq.Requirement.Required.String()
		if constraintStr == "" {
			diags = diags.Append(tfdiags.Sourceless(
				tfdiags.Error,
				"Missing provider version constraint",
				fmt.Sprintf("Runbook provider %s must declare a version constraint in required_providers.", providerReq.Type.ForDisplay()),
			))
			continue
		}
		constraints, err := providerreqs.ParseVersionConstraints(constraintStr)
		if err != nil {
			diags = diags.Append(tfdiags.Sourceless(
				tfdiags.Error,
				"Invalid provider version constraint",
				fmt.Sprintf("Runbook provider %s declares invalid version constraints %q: %s", providerReq.Type.ForDisplay(), constraintStr, err),
			))
			continue
		}
		reqs[providerReq.Type] = append(reqs[providerReq.Type], constraints...)
	}

	return reqs, diags
}

func (c *runbookCommandBase) discoverRunbookPaths(pwd string) (string, string, tfdiags.Diagnostics) {
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
		return "", "", diags.Append(tfdiags.Sourceless(tfdiags.Error, "No runbook configuration files", "Runbook command requires at least one .tfrun.hcl file in the current working directory."))
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

func (c *runbookCommandBase) collectRunbookVariableValues(config *runbookconfigs.RunbookConfig, vars *arguments.Vars) (terraform.InputValues, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if config == nil || vars == nil {
		return nil, nil
	}

	values, valueDiags := vars.CollectValues(func(filename string, src []byte) {})
	diags = diags.Append(valueDiags)
	declared, declaredDiags := backendrun.ParseVariableValues(values, config.Variables)
	diags = diags.Append(declaredDiags)
	return declared, diags
}
