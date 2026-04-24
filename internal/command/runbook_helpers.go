package command

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/backend/backendrun"
	backendLocal "github.com/hashicorp/terraform/internal/backend/local"
	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/command/workdir"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/depsfile"
	"github.com/hashicorp/terraform/internal/getproviders/providerreqs"
	"github.com/hashicorp/terraform/internal/providers"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runbookplanfile "github.com/hashicorp/terraform/internal/runbooks/runbookplanfile"
	"github.com/hashicorp/terraform/internal/states"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

const (
	runbookDependencyLockFilename     = ".tfrun.lock.hcl"
	runbookDataDirName                = ".tfrun"
	runbookDependencyMetadataFilename = "dependency-metadata.json"
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

// loadRunbook assembles the two different roots that a runbook command needs.
//
// The runbook directory is the dependency root for all runbook-owned artifacts
// such as .tfrun/, .tfrun.lock.hcl, and runbook provider installation.
//
// The workspace directory is the backend/state root for all workspace-owned
// concerns such as backend configuration, cloud configuration, workspace
// selection, and workspace state lookup for workspace.* references.
//
// Runbook commands must never create an independent backend namespace for the
// runbook directory. Instead, they borrow the enclosing root module's backend
// configuration and read workspace state through that backend while keeping
// runbook dependency state isolated under the runbook directory.
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
	config, parseDiags := loadRunbookConfigWithWorkspace(parser, runbookDir, workspaceDir)
	diags = diags.Append(parseDiags)
	diags = diags.Append(validateDeclaredRunbookProviderUsage(config))
	if diags.HasErrors() {
		return nil, diags
	}
	if c.testingOverrides == nil {
		workspaceLocks, workspaceLockDiags := workspaceLockedDependencies(workspaceDir)
		diags = diags.Append(workspaceLockDiags)
		diags = diags.Append(validateRunbookDependencyMetadata(runbookDir, config, workspaceLocks))
		if diags.HasErrors() {
			return nil, diags
		}
	}

	providerFactories, err := c.runbookProviderFactories(runbookDir)
	if err != nil {
		return nil, diags.Append(err)
	}

	workspaceState, workspaceStateDiags := c.loadRunbookWorkspaceState(workspaceDir, arguments.ViewHuman)
	diags = diags.Append(workspaceStateDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	return &loadedRunbook{
		RunbookDir:        runbookDir,
		WorkspaceDir:      workspaceDir,
		Config:            config,
		ProviderFactories: providerFactories,
		WorkspaceState:    workspaceState,
	}, diags
}

func loadRunbookConfigWithWorkspace(parser *runbookconfigs.RunbookParser, runbookDir, workspaceDir string) (*runbookconfigs.RunbookConfig, hcl.Diagnostics) {
	config, diags := parser.LoadRunbookConfigDir(runbookDir)
	if config == nil {
		return nil, diags
	}

	workspaceConfig, workspaceDiags := parser.LoadWorkspaceReferencesConfig(workspaceDir)
	diags = append(diags, workspaceDiags...)
	config.WorkspaceConfig = workspaceConfig
	config.WorkspaceSourceDir = workspaceDir
	return config, diags
}

func (c *runbookCommandBase) loadSavedRunbookPlan(path string) (*runbookplanfile.Plan, *runbookconfigs.RunbookConfig, map[terraformaddrs.Provider]providers.Factory, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	var err error
	if c.pluginPath == nil {
		if c.pluginPath, err = c.loadPluginPath(); err != nil {
			return nil, nil, nil, diags.Append(err)
		}
	}
	saved, err := runbookplanfile.Read(path)
	if err != nil {
		return nil, nil, nil, diags.Append(err)
	}
	c.View.SetConfigSources(func() map[string][]byte {
		return copyConfigSources(saved.Sources)
	})
	parser := runbookconfigs.NewRunbookParser(nil)
	config, parseDiags := parser.LoadRunbookConfigSources(saved.RunbookSourceDir, saved.Sources)
	diags = diags.Append(parseDiags)
	diags = diags.Append(validateDeclaredRunbookProviderUsage(config))
	if diags.HasErrors() {
		return nil, nil, nil, diags
	}
	var providerFactories map[terraformaddrs.Provider]providers.Factory
	if len(saved.RunbookLockFile) != 0 {
		locks, lockDiags := depsfile.LoadLocksFromBytes(saved.RunbookLockFile, runbookDependencyLockFilename)
		diags = diags.Append(lockDiags)
		if diags.HasErrors() {
			return nil, nil, nil, diags
		}
		locks = c.annotateDependencyLocksWithOverrides(locks)
		meta := c.runbookMeta(saved.RunbookSourceDir)
		providerFactories, err = meta.ProviderFactoriesFromLocks(locks)
	} else {
		providerFactories, err = c.runbookProviderFactories(saved.RunbookSourceDir)
	}
	if err != nil {
		return nil, nil, nil, diags.Append(err)
	}
	return saved, config, providerFactories, diags
}

func copyConfigSources(in map[string][]byte) map[string][]byte {
	ret := make(map[string][]byte, len(in))
	for k, v := range in {
		copyV := make([]byte, len(v))
		copy(copyV, v)
		ret[k] = copyV
	}
	return ret
}

func readOptionalFile(path string) []byte {
	if path == "" {
		return nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	copySrc := make([]byte, len(src))
	copy(copySrc, src)
	return copySrc
}

func runbookConfigSources(runbookDir string) map[string][]byte {
	ret := map[string][]byte{}
	paths, err := runbookConfigFilePaths(runbookDir)
	if err != nil {
		return ret
	}
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		ret[path] = src
	}
	return ret
}

func runbookConfigFilePaths(runbookDir string) ([]string, error) {
	var paths []string
	if err := filepath.Walk(runbookDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == runbookDir {
			return nil
		}

		name := info.Name()
		if strings.HasPrefix(name, ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(name, ".tfrun.hcl") {
			return nil
		}

		paths = append(paths, path)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

type runbookDeclaredProviderMetadata struct {
	Source             string `json:"source,omitempty"`
	Provider           string `json:"provider,omitempty"`
	VersionConstraints string `json:"version_constraints,omitempty"`
}

type runbookWorkspaceProviderRef struct {
	Reference string `json:"reference,omitempty"`
	Provider  string `json:"provider,omitempty"`
}

type runbookDependencyMetadata struct {
	DeclaredProviders           map[string]runbookDeclaredProviderMetadata `json:"declared_providers,omitempty"`
	WorkspaceProviderRefs       []runbookWorkspaceProviderRef              `json:"workspace_provider_refs,omitempty"`
	WorkspaceProviderSelections map[string]string                          `json:"workspace_provider_selections,omitempty"`
}

func runbookDependencyLockPath(runbookDir string) string {
	return filepath.Join(runbookDir, runbookDependencyLockFilename)
}

func runbookDataDirPath(runbookDir string) string {
	return filepath.Join(runbookDir, runbookDataDirName)
}

func runbookDependencyMetadataPath(runbookDir string) string {
	return filepath.Join(runbookDataDirPath(runbookDir), runbookDependencyMetadataFilename)
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

// workspaceMeta returns a Meta scoped to the discovered Terraform workspace
// root rather than the current runbook directory.
//
// Runbooks intentionally do not own backend configuration or workspace state.
// Any backend or cloud interaction for workspace.* references must therefore be
// performed through a Meta rooted at the enclosing Terraform configuration.
func (c *runbookCommandBase) workspaceMeta(workspaceDir string) Meta {
	meta := c.Meta
	wd := workdir.NewDir(workspaceDir)
	if c.WorkingDir != nil {
		wd.OverrideOriginalWorkingDir(c.WorkingDir.OriginalWorkingDir())
	}
	meta.WorkingDir = wd
	return meta
}

// loadRunbookWorkspaceState reads the current workspace state by reusing the
// enclosing Terraform root module's backend configuration.
//
// This creates only a transient in-memory backend handle for the workspace
// root. It does not create a runbook-owned backend, does not redirect backend
// state into .tfrun/, and does not establish an independent backend namespace
// for the runbook directory.
func (c *runbookCommandBase) loadRunbookWorkspaceState(workspaceDir string, viewType arguments.ViewType) (*states.State, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	workspaceMeta := c.workspaceMeta(workspaceDir)
	b, backendDiags := workspaceMeta.backend(workspaceDir, viewType)
	diags = diags.Append(backendDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	anchorWorkspaceLocalBackendPaths(b, workspaceDir)
	workspaceMeta.ignoreRemoteVersionConflict(b)

	workspaceName, err := workspaceMeta.Workspace()
	if err != nil {
		return nil, diags.Append(err)
	}
	stateFile, err := getStateFromBackend(b, workspaceName)
	if err != nil {
		return nil, diags.Append(err)
	}
	if stateFile == nil {
		return nil, diags
	}
	return stateFile.State, diags
}

// anchorWorkspaceLocalBackendPaths preserves root-module local-backend
// semantics when a runbook command is launched from a subdirectory.
//
// The backend instance itself still comes from the workspace root module's
// backend or cloud configuration. This helper only ensures that local-backend
// relative paths continue to resolve from workspaceDir rather than being
// accidentally re-anchored to the runbook directory or process cwd.
func anchorWorkspaceLocalBackendPaths(b backendrun.OperationsBackend, workspaceDir string) {
	localBackend, ok := b.(*backendLocal.Local)
	if !ok {
		return
	}

	if localBackend.StatePath == "" {
		localBackend.StatePath = filepath.Join(workspaceDir, backendLocal.DefaultStateFilename)
	} else if !filepath.IsAbs(localBackend.StatePath) {
		localBackend.StatePath = filepath.Join(workspaceDir, localBackend.StatePath)
	}
	if localBackend.StateOutPath != "" && !filepath.IsAbs(localBackend.StateOutPath) {
		localBackend.StateOutPath = filepath.Join(workspaceDir, localBackend.StateOutPath)
	}
	if localBackend.StateBackupPath != "" && localBackend.StateBackupPath != "-" && !filepath.IsAbs(localBackend.StateBackupPath) {
		localBackend.StateBackupPath = filepath.Join(workspaceDir, localBackend.StateBackupPath)
	}
	if localBackend.StateWorkspaceDir == "" {
		localBackend.StateWorkspaceDir = filepath.Join(workspaceDir, backendLocal.DefaultWorkspaceDir)
	} else if !filepath.IsAbs(localBackend.StateWorkspaceDir) {
		localBackend.StateWorkspaceDir = filepath.Join(workspaceDir, localBackend.StateWorkspaceDir)
	}
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

func collectRunbookWorkspaceProviderRefs(config *runbookconfigs.RunbookConfig) []runbookWorkspaceProviderRef {
	seen := map[string]runbookWorkspaceProviderRef{}
	addRef := func(reference string, provider terraformaddrs.Provider) {
		if provider == (terraformaddrs.Provider{}) {
			return
		}
		key := reference + "|" + provider.String()
		seen[key] = runbookWorkspaceProviderRef{Reference: reference, Provider: provider.ForDisplay()}
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
			addRef(target.String(), module.ProviderForLocalConfig(resource.ProviderConfigAddr()))
		case runbookaddrs.WorkspaceAction:
			action, module := workspaceActionConfigWithModule(config, target)
			if action == nil || module == nil {
				return
			}
			addRef(target.String(), module.ProviderForLocalConfig(action.ProviderConfigAddr()))
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
	if config != nil {
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
	}
	ret := make([]runbookWorkspaceProviderRef, 0, len(seen))
	for _, ref := range seen {
		ret = append(ret, ref)
	}
	sort.Slice(ret, func(i, j int) bool {
		if ret[i].Reference != ret[j].Reference {
			return ret[i].Reference < ret[j].Reference
		}
		return ret[i].Provider < ret[j].Provider
	})
	return ret
}

func generateRunbookDependencyMetadata(config *runbookconfigs.RunbookConfig, workspaceLocks *depsfile.Locks) runbookDependencyMetadata {
	meta := runbookDependencyMetadata{
		DeclaredProviders:           map[string]runbookDeclaredProviderMetadata{},
		WorkspaceProviderSelections: map[string]string{},
	}
	if config != nil && config.ProviderRequirements != nil {
		localNames := make([]string, 0, len(config.ProviderRequirements.RequiredProviders))
		for localName := range config.ProviderRequirements.RequiredProviders {
			localNames = append(localNames, localName)
		}
		sort.Strings(localNames)
		for _, localName := range localNames {
			providerReq := config.ProviderRequirements.RequiredProviders[localName]
			if providerReq == nil {
				continue
			}
			meta.DeclaredProviders[localName] = runbookDeclaredProviderMetadata{
				Source:             providerReq.Source,
				Provider:           providerReq.Type.ForDisplay(),
				VersionConstraints: providerReq.Requirement.Required.String(),
			}
		}
	}
	meta.WorkspaceProviderRefs = collectRunbookWorkspaceProviderRefs(config)
	if workspaceLocks != nil {
		for _, ref := range meta.WorkspaceProviderRefs {
			providerAddr, diags := terraformaddrs.ParseProviderSourceString(ref.Provider)
			if diags.HasErrors() {
				continue
			}
			if lock := workspaceLocks.Provider(providerAddr); lock != nil {
				meta.WorkspaceProviderSelections[ref.Provider] = lock.Version().String()
			}
		}
	}
	if len(meta.DeclaredProviders) == 0 {
		meta.DeclaredProviders = nil
	}
	if len(meta.WorkspaceProviderRefs) == 0 {
		meta.WorkspaceProviderRefs = nil
	}
	if len(meta.WorkspaceProviderSelections) == 0 {
		meta.WorkspaceProviderSelections = nil
	}
	return meta
}

func writeRunbookDependencyMetadata(runbookDir string, meta runbookDependencyMetadata) tfdiags.Diagnostics {
	if err := os.MkdirAll(runbookDataDirPath(runbookDir), 0o755); err != nil {
		return tfdiags.Diagnostics{}.Append(err)
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return tfdiags.Diagnostics{}.Append(err)
	}
	if err := os.WriteFile(runbookDependencyMetadataPath(runbookDir), raw, 0o644); err != nil {
		return tfdiags.Diagnostics{}.Append(err)
	}
	return nil
}

func validateRunbookDependencyMetadata(runbookDir string, config *runbookconfigs.RunbookConfig, workspaceLocks *depsfile.Locks) tfdiags.Diagnostics {
	path := runbookDependencyMetadataPath(runbookDir)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Runbook dependencies are out of date",
			"Runbook dependencies have not been initialized. Re-run 'terraform runbook init'.",
		))
	}
	if err != nil {
		return tfdiags.Diagnostics{}.Append(err)
	}
	var recorded runbookDependencyMetadata
	if err := json.Unmarshal(raw, &recorded); err != nil {
		return tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Invalid runbook dependency metadata",
			fmt.Sprintf("The runbook dependency metadata file %s could not be read: %s", path, err),
		))
	}
	current := generateRunbookDependencyMetadata(config, workspaceLocks)
	if !reflect.DeepEqual(recorded, current) {
		return tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Runbook dependencies are out of date",
			"Runbook dependencies no longer match the current runbook or workspace configuration. Re-run 'terraform runbook init'.",
		))
	}
	return nil
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
	paths, err := runbookConfigFilePaths(pwd)
	if err != nil {
		return "", "", diags.Append(err)
	}
	if len(paths) == 0 {
		return "", "", diags.Append(tfdiags.Sourceless(tfdiags.Error, "No runbook configuration files", "Runbook command requires at least one .tfrun.hcl file in the current runbook directory or its subdirectories."))
	}

	workspaceDir := discoverRunbookWorkspaceDir(pwd)
	parser := configs.NewParser(nil)
	if pwd == workspaceDir && parser.IsConfigDir(pwd) {
		return "", "", diags.Append(tfdiags.Sourceless(tfdiags.Error, "Runbook directory must not be the workspace root", "Runbook commands require a dedicated subdirectory. Move the .tfrun.hcl files into a runbook directory such as runbooks/<name>/ and run the command there, or use the global -chdir flag."))
	}

	return pwd, workspaceDir, diags
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
