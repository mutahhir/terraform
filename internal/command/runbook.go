// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package command

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	ctymsgpack "github.com/zclconf/go-cty/cty/msgpack"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configload"
	"github.com/hashicorp/terraform/internal/plans"
	"github.com/hashicorp/terraform/internal/rpcapi"
	"github.com/hashicorp/terraform/internal/rpcapi/terraform1"
	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/dependencies"
	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/runbooks"
	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/setup"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/runbooks/runbookeval"
	"github.com/hashicorp/terraform/internal/runbooks/runbookplan"
	"github.com/hashicorp/terraform/internal/runbooks/runbookplanfile"
	"github.com/hashicorp/terraform/internal/states"
	"github.com/hashicorp/terraform/internal/states/statefile"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type RunbookCommand struct {
	Meta
}

func (c *RunbookCommand) Help() string {
	return strings.TrimSpace(`
Usage: terraform [global options] runbook <subcommand>

  init   Validate the runbook and initialize local runbook state.
  plan   Generate a multi-step runbook plan file from local runbook state.
	  execute Execute a persisted multi-step runbook plan file.
`)
}

func (c *RunbookCommand) Synopsis() string {
	return "Work with Terraform runbooks"
}

func (c *RunbookCommand) Run(args []string) int {
	args = c.Meta.process(args)
	if len(args) != 1 {
		c.Ui.Error(c.Help())
		return 1
	}

	switch args[0] {
	case "init":
		return c.runInit()
	case "plan":
		return c.runPlan()
	case "execute":
		return c.runExecute()
	default:
		c.Ui.Error(c.Help())
		return 1
	}
}

func (c *RunbookCommand) runInit() int {
	statePath := c.runbookStatePath()
	if state, err := c.loadRunbookState(); err == nil && state != nil && state.Status == "in_progress" {
		c.Ui.Error(fmt.Sprintf("An in-progress runbook state already exists at %s. Run 'terraform runbook plan' or remove it before starting again.", statePath))
		return 1
	}
	_ = c.removeRunbookState()

	ctx := context.Background()
	client, err := rpcapi.NewInternalClient(ctx, &setup.ClientCapabilities{})
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to start Terraform RPC client: %s", err))
		return 1
	}
	defer client.Close(ctx)
	core := &rpcapi.GRPCCoreClient{}
	_ = core
	if client.ServerCapabilities() == nil {
		c.Ui.Error("Failed to complete RPC handshake")
		return 1
	}

	configPath := c.WorkingDir.RootModuleDir()
	openResp, err := client.Runbooks().OpenRunbookConfiguration(ctx, &runbooks.OpenRunbookConfiguration_Request{ConfigPath: configPath})
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to open runbook configuration: %s", err))
		return 1
	}
	if len(openResp.Diagnostics) > 0 {
		for _, diag := range openResp.Diagnostics {
			c.Ui.Error(diag.Summary + ": " + diag.Detail)
		}
		if openResp.RunbookConfigHandle == 0 {
			return 1
		}
	}

	runtimeHandle, cleanupRuntime, err := c.openRunbookRuntimeInternal(ctx, client)
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to open runbook runtime: %s", err))
		return 1
	}
	defer cleanupRuntime()
	_ = runtimeHandle // initialization/validation side effect for now

	rawCfg, loadDiags := runbookconfig.LoadConfigDir(configPath)
	if loadDiags.HasErrors() {
		c.Ui.Error(loadDiags.Err().Error())
		return 1
	}
	stepDeps, depDiags := runbookconfig.AnalyzeDependencies(rawCfg)
	if depDiags.HasErrors() {
		c.Ui.Error(depDiags.Err().Error())
		return 1
	}
	stepNames := make([]string, 0, len(stepDeps.Dependencies))
	for name := range stepDeps.Dependencies {
		stepNames = append(stepNames, name)
	}
	sort.Strings(stepNames)
	ordered := dependencyOrderedSteps(stepNames, stepDeps.Dependencies)
	workspaceName, err := c.Workspace()
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to determine workspace: %s", err))
		return 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	state := &localRunbookState{
		Version:      1,
		Status:       "in_progress",
		ConfigPath:   configPath,
		Workspace:    workspaceName,
		CreatedAt:    now,
		UpdatedAt:    now,
		StepOrder:    ordered,
		Dependencies: stepDeps.Dependencies,
	}
	if err := c.saveRunbookState(state); err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to write runbook state: %s", err))
		return 1
	}

	c.Ui.Output("Runbook initialized.")
	c.Ui.Output(fmt.Sprintf("State: %s", statePath))
	c.Ui.Output(fmt.Sprintf("Steps: %s", strings.Join(ordered, " -> ")))
	return 0
}

func (c *RunbookCommand) runPlan() int {
	state, err := c.loadRunbookState()
	if err != nil {
		if os.IsNotExist(err) {
			c.Ui.Error("No runbook state exists. Run 'terraform runbook init' first.")
			return 1
		}
		c.Ui.Error(fmt.Sprintf("Failed to read runbook state: %s", err))
		return 1
	}

	ctx := context.Background()
	client, err := rpcapi.NewInternalClient(ctx, &setup.ClientCapabilities{})
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to start Terraform RPC client: %s", err))
		return 1
	}
	defer client.Close(ctx)
	if client.ServerCapabilities() == nil {
		c.Ui.Error("Failed to complete RPC handshake")
		return 1
	}

	openResp, err := client.Runbooks().OpenRunbookConfiguration(ctx, &runbooks.OpenRunbookConfiguration_Request{ConfigPath: state.ConfigPath})
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to open runbook configuration: %s", err))
		return 1
	}
	if len(openResp.Diagnostics) > 0 {
		for _, diag := range openResp.Diagnostics {
			c.Ui.Error(diag.Summary + ": " + diag.Detail)
		}
		if openResp.RunbookConfigHandle == 0 {
			return 1
		}
	}

	runbookRuntimeHandle, cleanupRuntime, err := c.openRunbookRuntimeInternal(ctx, client)
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to open runbook runtime: %s", err))
		return 1
	}
	defer cleanupRuntime()

	stepsResp, err := client.Runbooks().FindRunbookConfigurationSteps(ctx, &runbooks.FindRunbookConfigurationSteps_Request{
		RunbookConfigHandle: openResp.RunbookConfigHandle,
	})
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to inspect runbook steps: %s", err))
		return 1
	}

	rawCfg, loadDiags := runbookconfig.LoadConfigDir(state.ConfigPath)
	if loadDiags.HasErrors() {
		c.Ui.Error(loadDiags.Err().Error())
		return 1
	}
	rawSteps := make(map[string]*runbookconfig.Step)
	for _, file := range rawCfg.Files {
		for name, step := range file.Steps {
			rawSteps[name] = step
		}
	}
	varScope := buildRunbookVariableScope(rawCfg)
	workspaceScope, workspaceDiags := c.buildWorkspaceScope(ctx)
	if workspaceDiags.HasErrors() {
		c.Ui.Error(workspaceDiags.Err().Error())
		return 1
	}
	stateFile, stateFileDiags := c.buildWorkspaceStateFile(ctx)
	if stateFileDiags.HasErrors() {
		c.Ui.Error(stateFileDiags.Err().Error())
		return 1
	}
	_ = stateFile

	buildResult, buildDiags, buildErr := runbookplan.Build(rawCfg, state.ConfigPath, state.Workspace, state.StepOrder, state.Dependencies, varScope, workspaceScope, func(stepName string, scope runbookconfig.EvalScope) (runbookplan.StepPlanResult, error) {
		rawStep := rawSteps[stepName]
		step := stepsResp.Config.Steps[stepName]
		protoScope := &runbooks.EvalScope{Variables: dynamicProtoValue(scope.Variables), Steps: dynamicProtoValue(scope.Steps), Each: dynamicProtoValue(scope.Each)}
		protoScope.Workspace = dynamicProtoValue(scope.Workspace)
		planResp, err := client.Runbooks().PlanRunbookStep(ctx, &runbooks.PlanRunbookStep_Request{
			RunbookConfigHandle:  openResp.RunbookConfigHandle,
			RunbookRuntimeHandle: runbookRuntimeHandle,
			StepName:             stepName,
			Scope:                protoScope,
		})
		if err != nil {
			return runbookplan.StepPlanResult{}, err
		}
		loweredFiles := copyLoweredFiles(planResp)
		if rawStep != nil && len(loweredFiles) == 0 {
			loweredBundle, lowerDiags := runbookconfig.LowerStep(rawCfg, rawStep)
			if lowerDiags.HasErrors() {
				return runbookplan.StepPlanResult{}, lowerDiags.Err()
			}
			if loweredBundle != nil {
				loweredFiles = loweredBundle.Files
			}
		}
		outputs, outputDiags := plannedOutputsFromResponse(planResp)
		if outputDiags.HasErrors() {
			return runbookplan.StepPlanResult{}, outputDiags.Err()
		}
		queries := planRespQueries(planResp)
		return runbookplan.StepPlanResult{
			Outputs:        outputs,
			Queries:        queries,
			KnownSkipped:   planResp.GetStatus() == runbooks.StepStatus_STEP_STATUS_SKIPPED,
			SkipReason:     firstDiagnosticDetail(planResp.Diagnostics),
			PlannedActions: staticActionAddresses(rawCfg, rawStep),
			PlannedQueries: staticQueryAddresses(rawStep),
			PlannedData:    staticPlannedData(rawStep),
			OutputNames:    step.Outputs,
			LoweredFiles:   loweredFiles,
		}, nil
	})
	if buildErr != nil {
		c.Ui.Error(buildErr.Error())
		return 1
	}
	if buildDiags.HasErrors() {
		c.Ui.Error(buildDiags.Err().Error())
		return 1
	}
	manifest := buildResult.Manifest
	manifest.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	lowered := buildResult.Lowered

	planPath := c.runbookPlanPath()
	sources, sourceErr := c.readRunbookSourceFiles(state.ConfigPath)
	if sourceErr != nil {
		c.Ui.Error(fmt.Sprintf("Failed to read runbook source files: %s", sourceErr))
		return 1
	}
	if err := runbookplanfile.Create(planPath, runbookplanfile.CreateArgs{Plan: manifest, StateFile: stateFile, Lowered: lowered, Sources: sources}); err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to write runbook plan file: %s", err))
		return 1
	}

	for _, line := range strings.Split(formatPlanSummary(manifest), "\n") {
		if line == "" {
			c.Ui.Output("")
			continue
		}
		c.Ui.Output(line)
	}
	c.Ui.Output("")
	c.Ui.Output(fmt.Sprintf("Saved the runbook plan to: %s", planPath))
	return 0
}

func (c *RunbookCommand) runExecute() int {
	planPath := c.runbookPlanPath()
	r, err := runbookplanfile.Open(planPath)
	if err != nil {
		if os.IsNotExist(err) {
			c.Ui.Error("No runbook plan exists. Run 'terraform runbook plan' first.")
			return 1
		}
		c.Ui.Error(fmt.Sprintf("Failed to open runbook plan file: %s", err))
		return 1
	}
	defer r.Close()

	manifest, err := r.ReadPlan()
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to read runbook plan manifest: %s", err))
		return 1
	}
	sources, err := r.ReadSourceFiles()
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to read runbook plan sources: %s", err))
		return 1
	}
	stateFile, err := r.ReadStateFile()
	if err != nil && err != statefile.ErrNoState {
		c.Ui.Error(fmt.Sprintf("Failed to read embedded runbook state: %s", err))
		return 1
	}

	rawCfg, loadDiags := runbookconfig.LoadConfigSources(manifest.ConfigPath, sources)
	if loadDiags.HasErrors() {
		c.Ui.Error(loadDiags.Err().Error())
		return 1
	}

	ctx := context.Background()
	providerFactories, err := c.ProviderFactories()
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to initialize providers for runbook execute: %s", err))
		return 1
	}
	tfCtx, ctxDiags := terraform.NewContext(&terraform.ContextOpts{Parallelism: 1, Providers: providerFactories})
	if ctxDiags.HasErrors() {
		c.Ui.Error(ctxDiags.Err().Error())
		return 1
	}

	stepResults := runbookeval.NewStepResults()
	seedStepResultsFromPlan(manifest, stepResults)
	currentState := states.NewState()
	if stateFile != nil && stateFile.State != nil {
		currentState = stateFile.State.DeepCopy()
	}
	baseWorkspaceScope, workspaceDiags := c.buildWorkspaceScope(ctx)
	if workspaceDiags.HasErrors() {
		c.Ui.Error(workspaceDiags.Err().Error())
		return 1
	}
	c.Ui.Output("Runbook Apply")
	c.Ui.Output("")
	c.Ui.Output("Applying saved runbook plan in dependency order:")
	c.Ui.Output("")
	for _, stepName := range manifest.StepOrder {
		manifestStep := persistedRunbookStep(manifest, stepName)
		baseStepName := stepName
		if manifestStep != nil && manifestStep.BaseName != "" {
			baseStepName = manifestStep.BaseName
		}
		step := lookupRunbookStep(rawCfg, baseStepName)
		if step == nil {
			c.Ui.Error(fmt.Sprintf("Runbook plan references missing step %q.", stepName))
			return 1
		}

		loweredFiles, err := r.ReadLoweredStepFiles(stepName)
		if err != nil {
			c.Ui.Error(fmt.Sprintf("Failed to read lowered files for step %q: %s", stepName, err))
			return 1
		}
		if manifestStep != nil && manifestStep.ForEachExpression != "" {
			if loweredBundle, lowerDiags := runbookconfig.LowerStepInstance(rawCfg, step, eachScopeForManifestStep(manifestStep), countScopeForManifestStep(manifestStep)); lowerDiags.HasErrors() {
				c.Ui.Error(lowerDiags.Err().Error())
				return 1
			} else if loweredBundle != nil {
				loweredFiles = loweredBundle.Files
			}
		}
		planFiles := stripRunbookNamespaceOutputs(loweredFiles)
		if len(loweredFiles) == 0 {
			c.Ui.Output(fmt.Sprintf("  # %s", stepName))
			c.Ui.Output("  status = \"skipped\"")
			c.Ui.Output("  reason = \"no lowered Terraform bundle saved in plan\"")
			c.Ui.Output("")
			continue
		}
		if manifestStep != nil && manifestStep.KnownSkipped {
			c.Ui.Output(fmt.Sprintf("  # %s", stepName))
			c.Ui.Output("  status = \"skipped\"")
			if manifestStep.SkipReason != "" {
				c.Ui.Output(fmt.Sprintf("  reason = %q", manifestStep.SkipReason))
			}
			c.Ui.Output("")
			continue
		}

		preScope := runbookconfig.EvalScope{
			Variables: buildRunbookVariableScope(rawCfg),
			Steps:     stepResults.ScopeValue(),
			Count:     countScopeForManifestStep(manifestStep),
			Each:      eachScopeForManifestStep(manifestStep),
			Workspace: mergedWorkspaceScope(baseWorkspaceScope, currentState),
		}
		preEval := runbookconfig.EvaluateStepForPlan(step, preScope)
		if preEval.Status == runbookconfig.StepStatusSkipped {
			c.Ui.Output(fmt.Sprintf("  # %s", stepName))
			c.Ui.Output("  status = \"skipped\"")
			if preEval.Detail != "" {
				c.Ui.Output(fmt.Sprintf("  reason = %q", preEval.Detail))
			}
			c.Ui.Output("")
			continue
		}
		if preEval.Status == runbookconfig.StepStatusFailed {
			c.Ui.Error(formatRunbookEvalDiagnostics(stepName, preEval))
			return 1
		}

		planConfig, inputValues, configDiags := loadLoweredStepConfig(planFiles)
		if configDiags.HasErrors() {
			c.Ui.Error(configDiags.Err().Error())
			return 1
		}
		plannedStepNames := append([]string(nil), manifest.StepOrder...)
		trimmedPlanConfig := stripFutureStepOutputs(planConfig, plannedStepNames, stepName)
		hasQueries := hasLoweredQueryFiles(loweredFiles)
		_, planDiags := tfCtx.Plan(trimmedPlanConfig, currentState.DeepCopy(), &terraform.PlanOpts{
			Mode:         plans.NormalMode,
			Query:        hasQueries,
			SetVariables: inputValues,
		})
		if planDiags.HasErrors() {
			c.Ui.Error(planDiags.Err().Error())
			return 1
		}
		applyFiles := stripExecuteUnsafeFiles(planFiles)
		if hasApplyableConfig(applyFiles) {
			applyConfig, _, applyConfigDiags := loadLoweredStepConfig(applyFiles)
			if applyConfigDiags.HasErrors() {
				c.Ui.Error(applyConfigDiags.Err().Error())
				return 1
			}
			applyPlan, applyPlanDiags := tfCtx.Plan(applyConfig, currentState.DeepCopy(), &terraform.PlanOpts{
				Mode:         plans.NormalMode,
				Query:        false,
				SetVariables: inputValues,
			})
			if applyPlanDiags.HasErrors() {
				c.Ui.Error(applyPlanDiags.Err().Error())
				return 1
			}
			newState, applyDiags := tfCtx.Apply(applyPlan, applyConfig, &terraform.ApplyOpts{})
			if applyDiags.HasErrors() {
				c.Ui.Error(applyDiags.Err().Error())
				return 1
			}
			if newState != nil {
				currentState = newState
			}
		}

		stepOutputs := stepResults.Get(stepInstanceAddrFromManifestStep(manifestStep))
		if !(manifestStep != nil && len(step.Lists) > 0) {
			stepOutputs = mergeStepOutputs(evaluateStepOutputsFromSource(step, preScope), stepOutputs)
		}
		stepResults.Set(stepInstanceAddrFromManifestStep(manifestStep), stepOutputs)
		c.Ui.Output(fmt.Sprintf("  # %s", stepName))
		c.Ui.Output("  status = \"complete\"")
		if formatted := formatStepOutputs(stepName, stepOutputs); formatted != "" {
			for _, line := range strings.Split(formatted, "\n") {
				c.Ui.Output("  " + line)
			}
		}
		c.Ui.Output("")
	}

	if err := c.writeRunbookStateSnapshot(ctx, manifest.Workspace, currentState); err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to persist runbook state: %s", err))
		return 1
	}

	c.Ui.Output("Runbook apply complete.")
	return 0
}

func (c *RunbookCommand) openRunbookRuntimeInternal(ctx context.Context, client *rpcapi.Client) (int64, func(), error) {
	deps := client.Dependencies()
	cleanup := func() {}

	locks, diags := c.lockedDependencies()
	if diags.HasErrors() {
		return 0, cleanup, diags.Err()
	}
	providerSelections := make([]*terraform1.ProviderPackage, 0, len(locks.AllProviders()))
	for _, lock := range locks.AllProviders() {
		hashes := lock.AllHashes()
		hashStrs := make([]string, len(hashes))
		for i, hash := range hashes {
			hashStrs[i] = hash.String()
		}
		providerSelections = append(providerSelections, &terraform1.ProviderPackage{
			SourceAddr: lock.Provider().String(),
			Version:    lock.Version().String(),
			Hashes:     hashStrs,
		})
	}

	lockResp, err := deps.CreateDependencyLocks(ctx, &dependencies.CreateDependencyLocks_Request{ProviderSelections: providerSelections})
	if err != nil {
		return 0, cleanup, err
	}

	cacheResp, err := deps.OpenProviderPluginCache(ctx, &dependencies.OpenProviderPluginCache_Request{CacheDir: c.providerLocalCacheDir().BasePath()})
	if err != nil {
		if lockResp.DependencyLocksHandle != 0 {
			_, _ = deps.CloseDependencyLocks(ctx, &dependencies.CloseDependencyLocks_Request{DependencyLocksHandle: lockResp.DependencyLocksHandle})
		}
		return 0, cleanup, err
	}

	runtimeResp, err := client.Runbooks().OpenRunbookRuntime(ctx, &runbooks.OpenRunbookRuntime_Request{
		DependencyLocksHandle: lockResp.DependencyLocksHandle,
		ProviderCacheHandle:   cacheResp.ProviderCacheHandle,
	})
	if err != nil {
		if cacheResp.ProviderCacheHandle != 0 {
			_, _ = deps.CloseProviderPluginCache(ctx, &dependencies.CloseProviderPluginCache_Request{ProviderCacheHandle: cacheResp.ProviderCacheHandle})
		}
		if lockResp.DependencyLocksHandle != 0 {
			_, _ = deps.CloseDependencyLocks(ctx, &dependencies.CloseDependencyLocks_Request{DependencyLocksHandle: lockResp.DependencyLocksHandle})
		}
		return 0, cleanup, err
	}

	cleanup = func() {
		if runtimeResp.RunbookRuntimeHandle != 0 {
			_, _ = client.Runbooks().CloseRunbookRuntime(ctx, &runbooks.CloseRunbookRuntime_Request{RunbookRuntimeHandle: runtimeResp.RunbookRuntimeHandle})
		}
		if cacheResp.ProviderCacheHandle != 0 {
			_, _ = deps.CloseProviderPluginCache(ctx, &dependencies.CloseProviderPluginCache_Request{ProviderCacheHandle: cacheResp.ProviderCacheHandle})
		}
		if lockResp.DependencyLocksHandle != 0 {
			_, _ = deps.CloseDependencyLocks(ctx, &dependencies.CloseDependencyLocks_Request{DependencyLocksHandle: lockResp.DependencyLocksHandle})
		}
	}

	return runtimeResp.RunbookRuntimeHandle, cleanup, nil
}

func (c *RunbookCommand) buildWorkspaceScope(ctx context.Context) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	b, backendDiags := c.Backend(&BackendOpts{Init: true})
	diags = diags.Append(backendDiags)
	if backendDiags.HasErrors() {
		return cty.EmptyObjectVal, diags
	}
	workspaceName, err := c.Workspace()
	if err != nil {
		return cty.EmptyObjectVal, diags.Append(err)
	}
	stateMgr, stateDiags := b.StateMgr(workspaceName)
	diags = diags.Append(stateDiags)
	if stateDiags.HasErrors() {
		return cty.EmptyObjectVal, diags
	}
	if err := stateMgr.RefreshState(); err != nil {
		return cty.EmptyObjectVal, diags.Append(err)
	}
	outputs, err := stateMgr.GetRootOutputValues(ctx)
	if err != nil {
		return cty.EmptyObjectVal, diags.Append(err)
	}
	outputVals := make(map[string]cty.Value, len(outputs))
	for name, output := range outputs {
		if output == nil {
			continue
		}
		outputVals[name] = output.Value
	}
	return cty.ObjectVal(map[string]cty.Value{"output": cty.ObjectVal(outputVals)}), diags
}

func (c *RunbookCommand) buildWorkspaceStateFile(ctx context.Context) (*statefile.File, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	b, backendDiags := c.Backend(&BackendOpts{Init: true})
	diags = diags.Append(backendDiags)
	if backendDiags.HasErrors() {
		return nil, diags
	}
	workspaceName, err := c.Workspace()
	if err != nil {
		return nil, diags.Append(err)
	}
	stateMgr, stateDiags := b.StateMgr(workspaceName)
	if stateDiags.HasErrors() {
		diags = diags.Append(stateDiags)
		return nil, diags
	}
	if err := stateMgr.RefreshState(); err != nil {
		return nil, diags.Append(err)
	}
	state := stateMgr.State()
	if state == nil {
		state = states.NewState()
	}
	outputs, err := stateMgr.GetRootOutputValues(ctx)
	if err != nil {
		return nil, diags.Append(err)
	}
	for name, output := range outputs {
		if output == nil {
			continue
		}
		state.RootOutputValues[name] = output
	}
	return &statefile.File{State: state}, diags
}

func buildRunbookVariableScope(cfg *runbookconfig.Config) cty.Value {
	if cfg == nil || len(cfg.Variables) == 0 {
		return cty.EmptyObjectVal
	}
	vals := make(map[string]cty.Value, len(cfg.Variables))
	for name, variable := range cfg.Variables {
		if variable != nil && variable.Default != nil {
			if val, diags := variable.Default.Value(&hcl.EvalContext{}); !diags.HasErrors() {
				vals[name] = val
				continue
			}
		}
		vals[name] = cty.NullVal(cty.DynamicPseudoType)
	}
	return cty.ObjectVal(vals)
}

func dependencyOrderedSteps(stepNames []string, deps map[string][]string) []string {
	remaining := make(map[string]bool, len(stepNames))
	for _, name := range stepNames {
		remaining[name] = true
	}
	ordered := make([]string, 0, len(stepNames))
	completed := map[string]bool{}
	for len(remaining) > 0 {
		var ready []string
		for _, name := range stepNames {
			if !remaining[name] {
				continue
			}
			ok := true
			for _, dep := range deps[name] {
				if !completed[dep] {
					ok = false
					break
				}
			}
			if ok {
				ready = append(ready, name)
			}
		}
		if len(ready) == 0 {
			for _, name := range stepNames {
				if remaining[name] {
					ordered = append(ordered, name)
				}
			}
			break
		}
		for _, name := range ready {
			ordered = append(ordered, name)
			completed[name] = true
			delete(remaining, name)
		}
	}
	return ordered
}

func dynamicProtoValue(v cty.Value) *runbooks.DynamicValue {
	if v == cty.NilVal {
		return nil
	}
	encoded, err := ctymsgpack.Marshal(v, cty.DynamicPseudoType)
	if err != nil {
		return nil
	}
	return &runbooks.DynamicValue{Msgpack: encoded}
}

func dynamicValueFromProto(protoVal *runbooks.DynamicValue) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if protoVal == nil || len(protoVal.Msgpack) == 0 {
		return cty.EmptyObjectVal, diags
	}
	v, err := ctymsgpack.Unmarshal(protoVal.Msgpack, cty.DynamicPseudoType)
	if err != nil {
		return cty.DynamicVal, diags.Append(err)
	}
	return v, diags
}

func staticActionAddresses(cfg *runbookconfig.Config, step *runbookconfig.Step) []string {
	planned := staticPlannedActions(cfg, step)
	ret := make([]string, 0, len(planned))
	for _, action := range planned {
		ret = append(ret, action.GetAddress())
	}
	return ret
}

func staticQueryAddresses(step *runbookconfig.Step) []string {
	planned := staticPlannedQueries(step)
	ret := make([]string, 0, len(planned))
	for _, query := range planned {
		ret = append(ret, query.GetAddress())
	}
	return ret
}

func staticPlannedActions(cfg *runbookconfig.Config, step *runbookconfig.Step) []*runbooks.PlanRunbookStep_PlannedAction {
	if step == nil {
		return nil
	}
	actionsByRef := make(map[string]*runbookconfig.Action)
	if cfg != nil {
		workspaceActions, _ := runbookconfig.WorkspaceActions(cfg)
		for ref, action := range workspaceActions {
			if action != nil {
				actionsByRef[ref] = action
			}
		}
		for ref, action := range cfg.Actions {
			if action != nil {
				actionsByRef[ref] = action
			}
		}
	}
	for _, action := range step.Actions {
		if action != nil {
			actionsByRef[action.Reference()] = action
		}
	}
	ret := make([]*runbooks.PlanRunbookStep_PlannedAction, 0, len(step.ExecuteInvokes))
	for _, invoke := range step.ExecuteInvokes {
		if invoke == nil {
			continue
		}
		action := actionsByRef[invoke.ActionRef]
		if action == nil {
			continue
		}
		ret = append(ret, &runbooks.PlanRunbookStep_PlannedAction{Address: invoke.ActionRef, ActionType: action.Type, ActionName: action.Name})
	}
	return ret
}

func staticPlannedQueries(step *runbookconfig.Step) []*runbooks.PlanRunbookStep_PlannedQuery {
	if step == nil {
		return nil
	}
	ret := make([]*runbooks.PlanRunbookStep_PlannedQuery, 0, len(step.Lists))
	for _, list := range step.Lists {
		if list == nil {
			continue
		}
		ret = append(ret, &runbooks.PlanRunbookStep_PlannedQuery{Address: fmt.Sprintf("list.%s.%s", list.Type, list.Name)})
	}
	return ret
}

func staticPlannedData(step *runbookconfig.Step) []string {
	if step == nil {
		return nil
	}
	ret := make([]string, 0, len(step.DataSources))
	for _, data := range step.DataSources {
		if data == nil {
			continue
		}
		ret = append(ret, data.Reference())
	}
	return ret
}

func (c *RunbookCommand) readRunbookSourceFiles(configPath string) (map[string][]byte, error) {
	entries, err := os.ReadDir(configPath)
	if err != nil {
		return nil, err
	}
	ret := make(map[string][]byte)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) != ".hcl" || !strings.HasSuffix(name, ".tfrun.hcl") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(configPath, name))
		if err != nil {
			return nil, err
		}
		ret[name] = src
	}
	return ret, nil
}

func lookupRunbookStep(cfg *runbookconfig.Config, stepName string) *runbookconfig.Step {
	if cfg == nil {
		return nil
	}
	for _, file := range cfg.Files {
		if step, ok := file.Steps[stepName]; ok {
			return step
		}
	}
	return nil
}

func persistedRunbookStep(plan *runbookplanfile.Plan, stepName string) *runbookplanfile.Step {
	if plan == nil {
		return nil
	}
	for i := range plan.Steps {
		if plan.Steps[i].Name == stepName {
			return &plan.Steps[i]
		}
	}
	return nil
}

func seedStepResultsFromPlan(plan *runbookplanfile.Plan, stepResults *runbookeval.StepResults) {
	if plan == nil || stepResults == nil {
		return
	}
	for i := range plan.Steps {
		step := &plan.Steps[i]
		outputs := decodePlannedStepOutputs(step)
		if outputs == cty.NilVal {
			continue
		}
		stepResults.Set(stepInstanceAddrFromManifestStep(step), outputs)
	}
}

func decodePlannedStepOutputs(step *runbookplanfile.Step) cty.Value {
	if step == nil || len(step.PlannedOutputs) == 0 {
		return cty.NilVal
	}
	ret := make(map[string]cty.Value, len(step.PlannedOutputs))
	for name, raw := range step.PlannedOutputs {
		v, err := ctymsgpack.Unmarshal(raw, cty.DynamicPseudoType)
		if err != nil {
			continue
		}
		ret[name] = v
	}
	if len(ret) == 0 {
		return cty.NilVal
	}
	return cty.ObjectVal(ret)
}

func firstDiagnosticDetail(diags []*terraform1.Diagnostic) string {
	if len(diags) == 0 || diags[0] == nil {
		return ""
	}
	return diags[0].Detail
}

func planRespQueries(resp *runbooks.PlanRunbookStep_Response) []runbookconfig.PlannedQuery {
	if resp == nil {
		return nil
	}
	ret := make([]runbookconfig.PlannedQuery, 0, len(resp.PlannedQueries))
	for _, query := range resp.PlannedQueries {
		if query == nil {
			continue
		}
		ret = append(ret, runbookconfig.PlannedQuery{
			Address: query.Address,
			Count:   int(query.ResultCount),
		})
	}
	return ret
}

func plannedOutputsFromResponse(resp *runbooks.PlanRunbookStep_Response) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if resp == nil || len(resp.PlannedOutputs) == 0 {
		return cty.EmptyObjectVal, diags
	}
	ret := make(map[string]cty.Value, len(resp.PlannedOutputs))
	for name, dyn := range resp.PlannedOutputs {
		v, moreDiags := dynamicValueFromProto(dyn)
		diags = diags.Append(moreDiags)
		if moreDiags.HasErrors() {
			continue
		}
		ret[name] = v
	}
	if len(ret) == 0 {
		return cty.EmptyObjectVal, diags
	}
	return cty.ObjectVal(ret), diags
}

func eachScopeForManifestStep(step *runbookplanfile.Step) cty.Value {
	if step == nil || step.ForEachExpression == "" {
		return cty.NilVal
	}
	key := cty.StringVal(step.ForEachKey)
	value := key
	if decoded, err := decodeManifestForEachValue(step); err == nil && decoded != cty.NilVal {
		value = decoded
	}
	return cty.ObjectVal(map[string]cty.Value{
		"key":   key,
		"value": value,
	})
}

func countScopeForManifestStep(step *runbookplanfile.Step) cty.Value {
	if step == nil || step.CountIndex == nil {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(map[string]cty.Value{
		"index": cty.NumberIntVal(int64(*step.CountIndex)),
	})
}

func decodeManifestForEachValue(step *runbookplanfile.Step) (cty.Value, error) {
	if step == nil || len(step.ForEachValue) == 0 {
		return cty.NilVal, nil
	}
	return ctymsgpack.Unmarshal(step.ForEachValue, cty.DynamicPseudoType)
}

func stepInstanceAddrFromManifestStep(step *runbookplanfile.Step) runbookaddrs.StepInstance {
	if step == nil {
		return runbookaddrs.Step{}.Instance(addrs.NoKey)
	}
	base := runbookaddrs.Step{Name: step.BaseName}
	if base.Name == "" {
		base.Name = step.Name
	}
	if step.ForEachExpression != "" {
		return base.Instance(addrs.StringKey(step.ForEachKey))
	}
	if step.CountIndex != nil {
		return base.Instance(addrs.IntKey(*step.CountIndex))
	}
	return base.Instance(addrs.NoKey)
}

func expandDependencyInstances(deps []string, plannedStepResults map[string]cty.Value) []string {
	ret := []string{}
	for _, dep := range deps {
		if val, ok := plannedStepResults[dep]; ok && val != cty.NilVal && val.Type().IsObjectType() {
			ret = append(ret, dep)
			continue
		}
		ret = append(ret, dep)
	}
	return ret
}

func formatRunbookEvalDiagnostics(stepName string, eval runbookconfig.StepEvaluation) string {
	if eval.Diags.HasErrors() {
		return fmt.Sprintf("Step %q failed: %s", stepName, eval.Diags.Err())
	}
	if eval.Detail != "" {
		return fmt.Sprintf("Step %q failed: %s", stepName, eval.Detail)
	}
	return fmt.Sprintf("Step %q failed.", stepName)
}

func loadLoweredStepConfig(files map[string][]byte) (*configs.Config, terraform.InputValues, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	dir, err := os.MkdirTemp("", "terraform-runbook-execute-")
	if err != nil {
		return nil, nil, diags.Append(err)
	}
	for name, src := range files {
		fullPath := filepath.Join(dir, name)
		if err := os.WriteFile(fullPath, src, 0o644); err != nil {
			return nil, nil, diags.Append(err)
		}
	}
	loader, err := configload.NewLoader(&configload.Config{ModulesDir: filepath.Join(dir, ".terraform", "modules"), IncludeQueryFiles: true})
	if err != nil {
		return nil, nil, diags.Append(err)
	}
	mod, hclDiags := loader.LoadRootModule(dir)
	diags = diags.Append(hclDiags)
	if mod == nil || hclDiags.HasErrors() {
		return nil, nil, diags
	}
	inputValues := make(terraform.InputValues)
	for name := range mod.Variables {
		inputValues[name] = &terraform.InputValue{Value: cty.NilVal, SourceType: terraform.ValueFromCaller}
	}
	config, buildDiags := terraform.BuildConfigWithGraph(mod, loader.ModuleWalker(), inputValues, configs.MockDataLoaderFunc(loader.LoadExternalMockData))
	diags = diags.Append(buildDiags)
	return config, inputValues, diags
}

func stripSyntheticConditionOutputs(files map[string][]byte) map[string][]byte {
	mainSrc, ok := files["main.tf"]
	if !ok {
		return files
	}
	parsed, diags := hclwrite.ParseConfig(mainSrc, "main.tf", hcl.InitialPos)
	if diags.HasErrors() || parsed == nil {
		return files
	}
	body := parsed.Body()
	blocks := body.Blocks()
	for _, block := range blocks {
		if block.Type() != "output" {
			continue
		}
		labels := block.Labels()
		if len(labels) != 1 || !strings.HasPrefix(labels[0], "__runbook_") {
			continue
		}
		attr := block.Body().GetAttribute("value")
		if attr == nil {
			continue
		}
		exprSrc := string(attr.Expr().BuildTokens(nil).Bytes())
		if strings.Contains(exprSrc, "workspace.output.") || strings.Contains(exprSrc, "steps.") {
			body.RemoveBlock(block)
		}
	}
	updated := make(map[string][]byte, len(files))
	for name, src := range files {
		updated[name] = src
	}
	updated["main.tf"] = hclwrite.Format(parsed.Bytes())
	return updated
}

func hasLoweredQueryFiles(files map[string][]byte) bool {
	for name := range files {
		if strings.HasSuffix(name, ".tfquery.hcl") {
			return true
		}
	}
	return false
}

func stripExecuteUnsafeFiles(files map[string][]byte) map[string][]byte {
	updated := make(map[string][]byte, len(files))
	for name, src := range files {
		updated[name] = src
	}
	delete(updated, "main.tfquery.hcl")
	updated["main.tf"] = stripSyntheticConditionOutputs(updated)["main.tf"]
	updated = stripListBackedOutputs(updated)
	return updated
}

func stripRunbookNamespaceOutputs(files map[string][]byte) map[string][]byte {
	mainSrc, ok := files["main.tf"]
	if !ok {
		return files
	}
	parsed, diags := hclwrite.ParseConfig(mainSrc, "main.tf", hcl.InitialPos)
	if diags.HasErrors() || parsed == nil {
		return files
	}
	body := parsed.Body()
	for _, block := range body.Blocks() {
		if block.Type() != "output" {
			continue
		}
		attr := block.Body().GetAttribute("value")
		if attr == nil {
			continue
		}
		exprSrc := string(attr.Expr().BuildTokens(nil).Bytes())
		if strings.Contains(exprSrc, "steps.") || strings.Contains(exprSrc, "workspace.output.") || strings.Contains(exprSrc, "actions.") {
			body.RemoveBlock(block)
		}
	}
	updated := make(map[string][]byte, len(files))
	for name, src := range files {
		updated[name] = src
	}
	updated["main.tf"] = hclwrite.Format(parsed.Bytes())
	return updated
}

func hasApplyableConfig(files map[string][]byte) bool {
	mainSrc, ok := files["main.tf"]
	if !ok {
		return false
	}
	parsed, diags := hclwrite.ParseConfig(mainSrc, "main.tf", hcl.InitialPos)
	if diags.HasErrors() || parsed == nil {
		return false
	}
	for _, block := range parsed.Body().Blocks() {
		switch block.Type() {
		case "action", "data", "provider", "terraform", "variable", "output":
			continue
		default:
			return true
		}
	}
	for _, block := range parsed.Body().Blocks() {
		if block.Type() == "action" || block.Type() == "data" {
			return true
		}
	}
	return false
}

func stripFutureStepOutputs(config *configs.Config, stepOrder []string, currentStep string) *configs.Config {
	if config == nil || config.Module == nil {
		return config
	}
	trimmed := *config
	trimmedModule := *config.Module
	trimmed.Module = &trimmedModule

	keepUntil := len(stepOrder)
	for i, name := range stepOrder {
		if name == currentStep {
			keepUntil = i
			break
		}
	}
	allowed := make(map[string]struct{}, keepUntil+1)
	for i := 0; i <= keepUntil && i < len(stepOrder); i++ {
		allowed[stepOrder[i]] = struct{}{}
	}

	trimmedOutputs := make(map[string]*configs.Output, len(config.Module.Outputs))
	for name, output := range config.Module.Outputs {
		if output == nil || strings.HasPrefix(name, "__runbook_") {
			if strings.HasPrefix(name, "__runbook_") && expressionReferencesFutureSteps(output.Expr, allowed) {
				continue
			}
			trimmedOutputs[name] = output
			continue
		}
		if !expressionReferencesFutureSteps(output.Expr, allowed) {
			trimmedOutputs[name] = output
		}
	}
	trimmed.Module.Outputs = trimmedOutputs
	return &trimmed
}

func expressionReferencesFutureSteps(expr hcl.Expression, allowed map[string]struct{}) bool {
	if expr == nil {
		return false
	}
	for _, traversal := range expr.Variables() {
		if len(traversal) < 2 {
			continue
		}
		root, ok := traversal[0].(hcl.TraverseRoot)
		if !ok || root.Name != "steps" {
			continue
		}
		stepAttr, ok := traversal[1].(hcl.TraverseAttr)
		if !ok {
			continue
		}
		if _, ok := allowed[stepAttr.Name]; !ok {
			return true
		}
	}
	return false
}

func evaluateStepOutputsFromSource(step *runbookconfig.Step, scope runbookconfig.EvalScope) cty.Value {
	if step == nil || len(step.Outputs) == 0 {
		return cty.EmptyObjectVal
	}
	scope = runbookconfig.ScopeWithStepLists(step, scope)
	vals := make(map[string]cty.Value, len(step.Outputs))
	for name, output := range step.Outputs {
		if output == nil || output.Value == nil {
			continue
		}
		val, diags := runbookconfig.EvalExpr(output.Value, scope, cty.DynamicPseudoType)
		if diags.HasErrors() {
			continue
		}
		vals[name] = val
	}
	if len(vals) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(vals)
}
func mergeStepOutputs(primary, fallback cty.Value) cty.Value {
	vals := map[string]cty.Value{}
	if fallback != cty.NilVal && fallback.Type().IsObjectType() {
		for name, val := range fallback.AsValueMap() {
			vals[name] = val
		}
	}
	if primary != cty.NilVal && primary.Type().IsObjectType() {
		for name, val := range primary.AsValueMap() {
			if !val.IsKnown() || val.IsNull() {
				if _, exists := vals[name]; exists {
					continue
				}
			}
			vals[name] = val
		}
	}
	if len(vals) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(vals)
}

func stripListBackedOutputs(files map[string][]byte) map[string][]byte {
	mainSrc, ok := files["main.tf"]
	if !ok {
		return files
	}
	parsed, diags := hclwrite.ParseConfig(mainSrc, "main.tf", hcl.InitialPos)
	if diags.HasErrors() || parsed == nil {
		return files
	}
	body := parsed.Body()
	for _, block := range body.Blocks() {
		if block.Type() != "output" {
			continue
		}
		attr := block.Body().GetAttribute("value")
		if attr == nil {
			continue
		}
		exprSrc := string(attr.Expr().BuildTokens(nil).Bytes())
		if strings.Contains(exprSrc, "list.") {
			body.RemoveBlock(block)
		}
	}
	updated := make(map[string][]byte, len(files))
	for name, src := range files {
		updated[name] = src
	}
	updated["main.tf"] = hclwrite.Format(parsed.Bytes())
	return updated
}

func stepOutputsFromState(state *states.State, step *runbookconfig.Step) cty.Value {
	if step == nil || state == nil || len(step.Outputs) == 0 {
		return cty.EmptyObjectVal
	}
	vals := make(map[string]cty.Value, len(step.Outputs))
	for name := range step.Outputs {
		if out := state.RootOutputValues[name]; out != nil {
			vals[name] = out.Value
		}
	}
	if len(vals) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(vals)
}

func workspaceScopeFromState(state *states.State) cty.Value {
	return workspaceScopeFromOutputs(rootOutputValuesFromState(state))
}

func rootOutputValuesFromState(state *states.State) map[string]cty.Value {
	outputs := make(map[string]cty.Value)
	if state != nil {
		for name, output := range state.RootOutputValues {
			if output == nil {
				continue
			}
			outputs[name] = output.Value
		}
	}
	return outputs
}

func workspaceScopeFromOutputs(outputs map[string]cty.Value) cty.Value {
	if outputs == nil {
		outputs = map[string]cty.Value{}
	}
	return cty.ObjectVal(map[string]cty.Value{"output": cty.ObjectVal(outputs)})
}

func mergedWorkspaceScope(base cty.Value, state *states.State) cty.Value {
	outputs := map[string]cty.Value{}
	if base != cty.NilVal && base.Type().IsObjectType() && base.Type().HasAttribute("output") {
		for name, val := range base.GetAttr("output").AsValueMap() {
			outputs[name] = val
		}
	}
	for name, val := range rootOutputValuesFromState(state) {
		outputs[name] = val
	}
	return workspaceScopeFromOutputs(outputs)
}

func (c *RunbookCommand) writeRunbookStateSnapshot(ctx context.Context, workspace string, state *states.State) error {
	_ = ctx
	b, diags := c.Backend(&BackendOpts{Init: true})
	if diags.HasErrors() {
		return diags.Err()
	}
	stateMgr, stateDiags := b.StateMgr(workspace)
	if stateDiags.HasErrors() {
		return stateDiags.Err()
	}
	if err := stateMgr.WriteState(state); err != nil {
		return err
	}
	if err := stateMgr.PersistState(nil); err != nil {
		return err
	}
	return nil
}
