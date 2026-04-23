package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/backend/backendrun"
	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/command/workdir"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/depsfile"
	"github.com/hashicorp/terraform/internal/getproviders/providerreqs"
	"github.com/hashicorp/terraform/internal/providers"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
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
	diags = diags.Append(validateDeclaredRunbookProviderUsage(config))
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

func workspaceDependencyLockPath(workspaceDir string) string {
	return filepath.Join(workspaceDir, dependencyLockFilename)
}

func workspaceLockedDependencies(workspaceDir string) (*depsfile.Locks, tfdiags.Diagnostics) {
	path := workspaceDependencyLockPath(workspaceDir)
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return depsfile.NewLocks(), nil
	}
	if err != nil {
		return nil, tfdiags.Diagnostics{}.Append(err)
	}
	return depsfile.LoadLocksFromFile(path)
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

func validateDeclaredRunbookProviderUsage(config *runbookconfigs.RunbookConfig) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if config == nil {
		return diags
	}

	hasRequiredProvider := func(localName string) bool {
		if config.ProviderRequirements == nil {
			return false
		}
		_, ok := config.ProviderRequirements.RequiredProviders[localName]
		return ok
	}

	for _, providerConfig := range config.ProviderConfigs {
		if providerConfig == nil {
			continue
		}
		if hasRequiredProvider(providerConfig.Name) {
			continue
		}
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Undeclared runbook provider",
			Detail:   fmt.Sprintf("Runbook provider %q must be declared in the runbook required_providers block before it can be configured.", providerConfig.Name),
			Subject:  providerConfig.DeclRange.Ptr(),
		})
	}

	checkUse := func(localName string, subject *hcl.Range, kind string) {
		if hasRequiredProvider(localName) {
			return
		}
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Undeclared runbook provider",
			Detail:   fmt.Sprintf("Runbook %s uses provider local name %q, but that provider is not declared in the runbook required_providers block.", kind, localName),
			Subject:  subject,
		})
	}

	for _, step := range config.Steps {
		if step == nil {
			continue
		}
		for _, action := range step.Actions {
			if action != nil {
				checkUse(action.ProviderConfigAddr().LocalName, &action.TypeRange, fmt.Sprintf("action %q", action.Addr().String()))
			}
		}
		for _, resource := range step.DataSources {
			if resource != nil {
				checkUse(resource.ProviderConfigAddr().LocalName, &resource.TypeRange, fmt.Sprintf("data block %q", resource.Addr().String()))
			}
		}
		for _, resource := range step.ListResources {
			if resource != nil {
				checkUse(resource.ProviderConfigAddr().LocalName, &resource.TypeRange, fmt.Sprintf("list block %q", resource.Addr().String()))
			}
		}
	}

	return diags
}

func runbookWorkspaceProviderRequirements(config *runbookconfigs.RunbookConfig) providerreqs.Requirements {
	reqs := make(providerreqs.Requirements)
	addProvider := func(provider terraformaddrs.Provider) {
		if provider == (terraformaddrs.Provider{}) {
			return
		}
		if _, exists := reqs[provider]; !exists {
			reqs[provider] = nil
		}
	}
	visitTraversal := func(traversal hcl.Traversal) {
		if len(traversal) == 0 {
			return
		}
		root, ok := traversal[0].(hcl.TraverseRoot)
		if !ok || root.Name != "workspace" {
			return
		}
		ref, diags := runbookaddrs.ParseRef(traversal)
		if diags.HasErrors() || ref == nil {
			return
		}
		switch target := ref.Subject.(type) {
		case runbookaddrs.WorkspaceResource:
			resource, module := workspaceResourceConfigWithModule(config, target)
			if resource == nil || module == nil {
				return
			}
			addProvider(module.ProviderForLocalConfig(resource.ProviderConfigAddr()))
		case runbookaddrs.WorkspaceAction:
			action, module := workspaceActionConfigWithModule(config, target)
			if action == nil || module == nil {
				return
			}
			addProvider(module.ProviderForLocalConfig(action.ProviderConfigAddr()))
		}
	}
	visitExpr := func(expr hcl.Expression) {
		if expr == nil {
			return
		}
		for _, traversal := range expr.Variables() {
			visitTraversal(traversal)
		}
	}
	var visitBody func(body hcl.Body)
	visitBody = func(body hcl.Body) {
		if body == nil {
			return
		}
		attrs, _ := body.JustAttributes()
		for _, attr := range attrs {
			visitExpr(attr.Expr)
		}
		content, _, _ := body.PartialContent(&hcl.BodySchema{})
		for _, block := range content.Blocks {
			visitBody(block.Body)
		}
	}

	if config == nil {
		return reqs
	}
	for _, output := range config.Outputs {
		if output != nil {
			visitExpr(output.Expr)
		}
	}
	for _, step := range config.Steps {
		if step == nil {
			continue
		}
		visitExpr(step.Count)
		visitExpr(step.ForEach)
		for _, local := range step.Locals {
			if local != nil {
				visitExpr(local.Expr)
			}
		}
		for _, output := range step.Outputs {
			if output != nil {
				visitExpr(output.Expr)
			}
		}
		for _, condition := range step.Preconditions {
			if condition != nil {
				visitExpr(condition.Condition)
				visitExpr(condition.ErrorMessage)
			}
		}
		for _, condition := range step.Postconditions {
			if condition != nil {
				visitExpr(condition.Condition)
				visitExpr(condition.ErrorMessage)
			}
		}
		for _, action := range step.Actions {
			if action != nil {
				visitExpr(action.Count)
				visitExpr(action.ForEach)
				visitBody(action.Config)
			}
		}
		for _, resource := range step.DataSources {
			if resource != nil {
				visitExpr(resource.Count)
				visitExpr(resource.ForEach)
				visitBody(resource.Config)
			}
		}
		for _, resource := range step.ListResources {
			if resource != nil {
				visitExpr(resource.Count)
				visitExpr(resource.ForEach)
				visitBody(resource.Config)
				if resource.List != nil {
					visitExpr(resource.List.IncludeResource)
					visitExpr(resource.List.Limit)
				}
			}
		}
		for _, execution := range step.Executions {
			if execution != nil {
				for _, traversal := range execution.InvokeAction {
					visitTraversal(traversal)
				}
			}
		}
	}
	return reqs
}

func workspaceResourceConfigWithModule(config *runbookconfigs.RunbookConfig, ref runbookaddrs.WorkspaceResource) (*configs.Resource, *configs.Module) {
	if config == nil || config.WorkspaceConfig == nil {
		return nil, nil
	}
	target := config.WorkspaceConfig
	for _, call := range ref.Module.Calls {
		child, ok := target.Children[call.Name]
		if !ok || child == nil {
			return nil, nil
		}
		target = child
	}
	if target.Module == nil {
		return nil, nil
	}
	resources := target.Module.ManagedResources
	if ref.Resource.Mode == terraformaddrs.DataResourceMode {
		resources = target.Module.DataResources
	}
	for _, resource := range resources {
		if resource != nil && resource.Type == ref.Resource.Type && resource.Name == ref.Resource.Name {
			return resource, target.Module
		}
	}
	return nil, target.Module
}

func workspaceActionConfigWithModule(config *runbookconfigs.RunbookConfig, addr runbookaddrs.WorkspaceAction) (*configs.Action, *configs.Module) {
	if config == nil || config.WorkspaceConfig == nil || config.WorkspaceConfig.Module == nil {
		return nil, nil
	}
	target := config.WorkspaceConfig
	for _, call := range addr.Module.Calls {
		child, ok := target.Children[call.Name]
		if !ok || child == nil {
			return nil, nil
		}
		target = child
	}
	if target.Module == nil {
		return nil, nil
	}
	return target.Module.Actions[addr.Action.String()], target.Module
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
