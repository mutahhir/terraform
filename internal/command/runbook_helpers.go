package command

import (
	"os"
	"path/filepath"
	"strings"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/backend/backendrun"
	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/providers"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/states"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
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

	providerFactories, err := c.ProviderFactories()
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
