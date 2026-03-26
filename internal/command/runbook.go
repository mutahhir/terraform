// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package command

import (
	"context"
	"errors"
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
	"github.com/hashicorp/terraform/internal/backend/backendrun"
	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configload"
	"github.com/hashicorp/terraform/internal/plans"
	"github.com/hashicorp/terraform/internal/providers"
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

type runbookStepPlanningFailedError struct {
	stepName string
}

type runbookPlanArgs struct {
	OutPath string
	Vars    arguments.Vars
}

type runbookExecuteArgs struct {
	PlanPath     string
	Vars         arguments.Vars
	AutoApprove  bool
	InputEnabled bool
}

type builtRunbookPlan struct {
	Manifest   *runbookplanfile.Plan
	Lowered    map[string]map[string][]byte
	SourceMaps map[string]map[string][]runbookconfig.SourceMapEntry
	Sources    map[string][]byte
	StateFile  *statefile.File
	Config     *runbookconfig.Config
}

type runbookActionOutputHook struct {
	terraform.NilHook
	outputs map[string]*strings.Builder
}

func (e runbookStepPlanningFailedError) Error() string {
	if e.stepName == "" {
		return "runbook step planning failed"
	}
	return fmt.Sprintf("runbook step %q planning failed", e.stepName)
}

func newRunbookActionOutputHook() *runbookActionOutputHook {
	return &runbookActionOutputHook{outputs: map[string]*strings.Builder{}}
}

func (h *runbookActionOutputHook) ProgressAction(id terraform.HookActionIdentity, progress string) (terraform.HookAction, error) {
	if h == nil {
		return terraform.HookActionContinue, nil
	}
	addr := id.Addr.String()
	b, ok := h.outputs[addr]
	if !ok {
		b = &strings.Builder{}
		h.outputs[addr] = b
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(progress)
	return terraform.HookActionContinue, nil
}

func (h *runbookActionOutputHook) ScopeValue() cty.Value {
	if h == nil || len(h.outputs) == 0 {
		return cty.EmptyObjectVal
	}
	byType := map[string]map[string]cty.Value{}
	for addr, buf := range h.outputs {
		parsed, diags := addrs.ParseAbsActionStr(addr)
		if diags.HasErrors() {
			continue
		}
		actionType := parsed.Action.Type
		actionName := parsed.Action.Name
		if _, ok := byType[actionType]; !ok {
			byType[actionType] = map[string]cty.Value{}
		}
		byType[actionType][actionName] = cty.ObjectVal(map[string]cty.Value{
			"output": cty.StringVal(buf.String()),
		})
	}
	if len(byType) == 0 {
		return cty.EmptyObjectVal
	}
	ret := map[string]cty.Value{}
	for actionType, byName := range byType {
		ret[actionType] = cty.ObjectVal(byName)
	}
	return cty.ObjectVal(ret)
}

func (c *RunbookCommand) Help() string {
	return strings.TrimSpace(`
Usage: terraform [global options] runbook <subcommand>

	  plan    Generate a multi-step runbook plan.
	  execute Execute a runbook plan or create one and execute it.

Runbook execute options:

	  -auto-approve  Skip interactive approval prompts.
	  -input=false   Disable interactive prompts.
`)
}

func (c *RunbookCommand) Synopsis() string {
	return "Work with Terraform runbooks"
}

func (c *RunbookCommand) Run(args []string) int {
	args = c.Meta.process(args)
	if len(args) < 1 {
		c.Ui.Error(c.Help())
		return 1
	}

	switch args[0] {
	case "plan":
		return c.runPlan(args[1:])
	case "execute":
		return c.runExecute(args[1:])
	default:
		c.Ui.Error(c.Help())
		return 1
	}
}

func (c *RunbookCommand) runPlan(args []string) int {
	parsed, diags := c.parseRunbookPlanArgs(args)
	if diags.HasErrors() {
		c.Ui.Error(diags.Err().Error())
		return 1
	}
	var varDiags tfdiags.Diagnostics
	c.VariableValues, varDiags = parsed.Vars.CollectValues(func(string, []byte) {})
	if varDiags.HasErrors() {
		c.Ui.Error(varDiags.Err().Error())
		return 1
	}
	built, ok := c.buildRunbookPlan(context.Background())
	if !ok {
		return 1
	}
	for _, line := range strings.Split(formatPlanSummary(c.Colorize(), built.Manifest), "\n") {
		if line == "" {
			c.Ui.Output("")
			continue
		}
		c.Ui.Output(line)
	}
	if parsed.OutPath != "" {
		if err := runbookplanfile.Create(parsed.OutPath, runbookplanfile.CreateArgs{Plan: built.Manifest, StateFile: built.StateFile, Lowered: built.Lowered, Sources: built.Sources}); err != nil {
			c.Ui.Error(fmt.Sprintf("Failed to write runbook plan file: %s", err))
			return 1
		}
		c.Ui.Output("")
		c.Ui.Output(c.Colorize().Color(fmt.Sprintf("[bold][green]Saved runbook plan:[reset] %s", parsed.OutPath)))
	}
	return 0
}

func (c *RunbookCommand) buildRunbookPlan(ctx context.Context) (*builtRunbookPlan, bool) {
	client, err := rpcapi.NewInternalClient(ctx, &setup.ClientCapabilities{})
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to start Terraform RPC client: %s", err))
		return nil, false
	}
	defer client.Close(ctx)
	if client.ServerCapabilities() == nil {
		c.Ui.Error("Failed to complete RPC handshake")
		return nil, false
	}
	configPath := c.WorkingDir.RootModuleDir()

	openResp, err := client.Runbooks().OpenRunbookConfiguration(ctx, &runbooks.OpenRunbookConfiguration_Request{ConfigPath: configPath})
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to open runbook configuration: %s", err))
		return nil, false
	}
	if len(openResp.Diagnostics) > 0 {
		for _, diag := range openResp.Diagnostics {
			c.Ui.Error(diag.Summary + ": " + diag.Detail)
		}
		if openResp.RunbookConfigHandle == 0 {
			return nil, false
		}
	}

	runbookRuntimeHandle, cleanupRuntime, err := c.openRunbookRuntimeInternal(ctx, client)
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to open runbook runtime: %s", err))
		return nil, false
	}
	defer cleanupRuntime()

	stepsResp, err := client.Runbooks().FindRunbookConfigurationSteps(ctx, &runbooks.FindRunbookConfigurationSteps_Request{
		RunbookConfigHandle: openResp.RunbookConfigHandle,
	})
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to inspect runbook steps: %s", err))
		return nil, false
	}

	rawCfg, loadDiags := runbookconfig.LoadConfigDir(configPath)
	if loadDiags.HasErrors() {
		c.Ui.Error(loadDiags.Err().Error())
		return nil, false
	}
	stepDeps, depDiags := runbookconfig.AnalyzeDependencies(rawCfg)
	if depDiags.HasErrors() {
		c.Ui.Error(depDiags.Err().Error())
		return nil, false
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
		return nil, false
	}
	rawSteps := make(map[string]*runbookconfig.Step)
	for _, file := range rawCfg.Files {
		for name, step := range file.Steps {
			rawSteps[name] = step
		}
	}
	varScope, runbookInputs, varDiags := c.loadRunbookVariableValues(rawCfg)
	if varDiags.HasErrors() {
		c.Ui.Error(varDiags.Err().Error())
		return nil, false
	}
	workspaceScope, workspaceDiags := c.buildWorkspaceScope(ctx)
	if workspaceDiags.HasErrors() {
		c.Ui.Error(workspaceDiags.Err().Error())
		return nil, false
	}
	providerFactories, providerErr := c.ProviderFactories()
	if providerErr != nil {
		c.Ui.Error(fmt.Sprintf("Failed to initialize providers for runbook plan: %s", providerErr))
		return nil, false
	}
	stateFile, stateFileDiags := c.buildWorkspaceStateFile(ctx)
	if stateFileDiags.HasErrors() {
		c.Ui.Error(stateFileDiags.Err().Error())
		return nil, false
	}

	buildResult, buildDiags, buildErr := runbookplan.Build(rawCfg, configPath, workspaceName, ordered, stepDeps.Dependencies, varScope, workspaceScope, func(stepName string, scope runbookconfig.EvalScope) (runbookplan.StepPlanResult, error) {
		rawStep := rawSteps[stepName]
		step := stepsResp.Config.Steps[stepName]
		c.Ui.Output(c.Colorize().Color(fmt.Sprintf("[cyan]Planning step:[reset] %s", stepName)))
		if scope.Each != cty.NilVal || scope.Count != cty.NilVal {
			return c.planRunbookStepInstance(stepName, rawCfg, rawStep, step, scope, runbookInputs, providerFactories)
		}
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
		c.showRunbookProtoDiagnostics(planResp.Diagnostics)
		if planResp.GetStatus() == runbooks.StepStatus_STEP_STATUS_FAILED {
			return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
		}
		loweredFiles := copyLoweredFiles(planResp)
		var loweredSourceMaps map[string][]runbookconfig.SourceMapEntry
		if rawStep != nil && len(loweredFiles) == 0 {
			loweredBundle, lowerDiags := runbookconfig.LowerStep(rawCfg, rawStep)
			if lowerDiags.HasErrors() {
				return runbookplan.StepPlanResult{}, lowerDiags.Err()
			}
			if loweredBundle != nil {
				loweredFiles = loweredBundle.Files
				loweredSourceMaps = loweredBundle.SourceMaps
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
			SourceMaps:     loweredSourceMaps,
		}, nil
	})
	if buildErr != nil {
		var stepErr runbookStepPlanningFailedError
		if errors.As(buildErr, &stepErr) {
			return nil, false
		}
		c.Ui.Error(buildErr.Error())
		return nil, false
	}
	if buildDiags.HasErrors() {
		c.Ui.Error(buildDiags.Err().Error())
		return nil, false
	}
	manifest := buildResult.Manifest
	manifest.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	lowered := buildResult.Lowered
	sources, sourceErr := c.readRunbookSourceFiles(configPath)
	if sourceErr != nil {
		c.Ui.Error(fmt.Sprintf("Failed to read runbook source files: %s", sourceErr))
		return nil, false
	}
	return &builtRunbookPlan{Manifest: manifest, Lowered: lowered, SourceMaps: collectManifestSourceMaps(manifest), Sources: sources, StateFile: stateFile, Config: rawCfg}, true
}

func (c *RunbookCommand) runExecute(args []string) int {
	parsed, diags := c.parseRunbookExecuteArgs(args)
	if diags.HasErrors() {
		c.Ui.Error(diags.Err().Error())
		return 1
	}
	c.Meta.input = parsed.InputEnabled
	var varDiags tfdiags.Diagnostics
	c.VariableValues, varDiags = parsed.Vars.CollectValues(func(string, []byte) {})
	if varDiags.HasErrors() {
		c.Ui.Error(varDiags.Err().Error())
		return 1
	}
	if parsed.PlanPath == "" {
		built, ok := c.buildRunbookPlan(context.Background())
		if !ok {
			return 1
		}
		c.showRunbookPlanSummary(built.Manifest)
		if !parsed.AutoApprove {
			if !c.Input() {
				c.Ui.Error("Runbook execute requires interactive approval unless -auto-approve is set.")
				return 1
			}
			approved, err := c.confirm(&terraform.InputOpts{
				Id:          "approve",
				Query:       "Do you want to execute this runbook plan?",
				Description: "Only 'yes' will be accepted to approve runbook execution.",
			})
			if err != nil {
				c.Ui.Error(err.Error())
				return 1
			}
			if !approved {
				c.Ui.Output("Runbook execution cancelled.")
				return 1
			}
		}
		return c.executeRunbookPlanData(built.Manifest, built.Sources, built.StateFile, collectManifestSourceMaps(built.Manifest), parsed.AutoApprove, func(stepName string) (map[string][]byte, error) {
			return built.Lowered[stepName], nil
		})
	}
	r, err := runbookplanfile.Open(parsed.PlanPath)
	if err != nil {
		if os.IsNotExist(err) {
			c.Ui.Error(fmt.Sprintf("No runbook plan exists at %s.", parsed.PlanPath))
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
	c.showRunbookPlanSummary(manifest)
	if !parsed.AutoApprove {
		if !c.Input() {
			c.Ui.Error("Runbook execute requires interactive approval unless -auto-approve is set.")
			return 1
		}
		approved, err := c.confirm(&terraform.InputOpts{
			Id:          "approve",
			Query:       "Do you want to execute this runbook plan?",
			Description: "Only 'yes' will be accepted to approve runbook execution.",
		})
		if err != nil {
			c.Ui.Error(err.Error())
			return 1
		}
		if !approved {
			c.Ui.Output("Runbook execution cancelled.")
			return 1
		}
	}
	return c.executeRunbookPlanData(manifest, sources, stateFile, collectManifestSourceMaps(manifest), parsed.AutoApprove, r.ReadLoweredStepFiles)
}

func (c *RunbookCommand) executeRunbookPlanData(manifest *runbookplanfile.Plan, sources map[string][]byte, stateFile *statefile.File, sourceMaps map[string]map[string][]runbookconfig.SourceMapEntry, autoApprove bool, loweredReader func(string) (map[string][]byte, error)) int {
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
	actionOutputHook := newRunbookActionOutputHook()
	tfCtx, ctxDiags := terraform.NewContext(&terraform.ContextOpts{Parallelism: 1, Providers: providerFactories, Hooks: []terraform.Hook{c.uiHook(), actionOutputHook}})
	if ctxDiags.HasErrors() {
		c.Ui.Error(ctxDiags.Err().Error())
		return 1
	}

	stepResults := runbookeval.NewStepResults()
	seedStepResultsFromPlan(manifest, stepResults)
	// originalWorkspaceState is the snapshot produced by normal Terraform apply.
	// Runbook execution must treat it as read-only and must not plan/apply
	// lowered step configs against it, because each lowered step is only a
	// partial configuration.
	originalWorkspaceState := states.NewState()
	if stateFile != nil && stateFile.State != nil {
		originalWorkspaceState = stateFile.State.DeepCopy()
	}
	varScope, runbookInputs, varDiags := c.loadRunbookVariableValues(rawCfg)
	if varDiags.HasErrors() {
		c.Ui.Error(varDiags.Err().Error())
		return 1
	}
	baseWorkspaceScope, workspaceDiags := c.buildWorkspaceScope(ctx)
	if workspaceDiags.HasErrors() {
		c.Ui.Error(workspaceDiags.Err().Error())
		return 1
	}
	c.Ui.Output(c.Colorize().Color("[bold]Runbook Apply[reset]"))
	c.Ui.Output("")
	c.Ui.Output(c.Colorize().Color("[cyan]Applying saved runbook plan in dependency order:[reset]"))
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

		loweredFiles, err := loweredReader(stepName)
		if err != nil {
			c.Ui.Error(fmt.Sprintf("Failed to read lowered files for step %q: %s", stepName, err))
			return 1
		}
		if loweredBundle, lowerDiags := runbookconfig.LowerStepInstanceWithScope(rawCfg, step, runbookconfig.EvalScope{
			Variables: varScope,
			Steps:     stepResults.ScopeValue(),
			Workspace: mergedWorkspaceScope(baseWorkspaceScope, originalWorkspaceState),
			Each:      eachScopeForManifestStep(manifestStep),
			Count:     countScopeForManifestStep(manifestStep),
		}); lowerDiags.HasErrors() {
			c.showDiagnostics(lowerDiags)
			return 1
		} else if loweredBundle != nil {
			loweredFiles = loweredBundle.Files
			if sourceMaps == nil {
				sourceMaps = map[string]map[string][]runbookconfig.SourceMapEntry{}
			}
			sourceMaps[stepName] = loweredBundle.SourceMaps
		}
		planFiles := loweredFiles
		if len(loweredFiles) == 0 {
			c.Ui.Output(c.Colorize().Color(fmt.Sprintf("[bold][cyan]# %s[reset]", stepName)))
			c.Ui.Output(c.Colorize().Color("status = [yellow]\"skipped\"[reset]"))
			c.Ui.Output("reason = \"no lowered Terraform bundle saved in plan\"")
			c.Ui.Output("")
			continue
		}
		if manifestStep != nil && manifestStep.KnownSkipped {
			c.Ui.Output(c.Colorize().Color(fmt.Sprintf("[bold][cyan]# %s[reset]", stepName)))
			c.Ui.Output(c.Colorize().Color("status = [yellow]\"skipped\"[reset]"))
			if manifestStep.SkipReason != "" {
				c.Ui.Output(fmt.Sprintf("reason = %q", manifestStep.SkipReason))
			}
			c.Ui.Output("")
			continue
		}

		preScope := runbookconfig.EvalScope{
			Variables: varScope,
			Steps:     stepResults.ScopeValue(),
			Count:     countScopeForManifestStep(manifestStep),
			Each:      eachScopeForManifestStep(manifestStep),
			Actions:   actionOutputHook.ScopeValue(),
			Workspace: mergedWorkspaceScope(baseWorkspaceScope, originalWorkspaceState),
		}
		preEval := runbookconfig.EvaluateStepForPlan(step, preScope)
		if preEval.Status == runbookconfig.StepStatusSkipped {
			c.Ui.Output(c.Colorize().Color(fmt.Sprintf("[bold][cyan]# %s[reset]", stepName)))
			c.Ui.Output(c.Colorize().Color("status = [yellow]\"skipped\"[reset]"))
			if preEval.Detail != "" {
				c.Ui.Output(fmt.Sprintf("reason = %q", preEval.Detail))
			}
			c.Ui.Output("")
			continue
		}
		if preEval.Status == runbookconfig.StepStatusFailed {
			c.Ui.Error(formatRunbookEvalDiagnostics(stepName, preEval))
			return 1
		}

		planConfig, inputValues, configDiags := loadLoweredStepConfig(planFiles, runbookInputs)
		if len(configDiags) != 0 {
			c.showRemappedDiagnostics(configDiags, sourceMaps[stepName])
		}
		if configDiags.HasErrors() {
			return 1
		}
		plannedStepNames := append([]string(nil), manifest.StepOrder...)
		trimmedPlanConfig := stripFutureStepOutputs(planConfig, plannedStepNames, stepName)
		hasQueries := hasLoweredQueryFiles(loweredFiles)
		stepState := states.NewState()
		tfPlan, planDiags := tfCtx.Plan(trimmedPlanConfig, stepState, &terraform.PlanOpts{
			Mode:         plans.NormalMode,
			Query:        hasQueries,
			SetVariables: inputValues,
		})
		if len(planDiags) != 0 {
			c.showRemappedDiagnostics(planDiags, sourceMaps[stepName])
		}
		if planDiags.HasErrors() {
			return 1
		}
		if !autoApprove {
			preview := formatStepExecutionPreview(c.Colorize(), manifestStep, plannedOutputValuesFromPlan(tfPlan))
			if preview != "" {
				for _, line := range strings.Split(preview, "\n") {
					c.Ui.Output(line)
				}
				c.Ui.Output("")
			}
			approved, err := c.confirm(&terraform.InputOpts{
				Id:          "runbook-step-approve-" + stepName,
				Query:       fmt.Sprintf("Execute step %q?", stepName),
				Description: "Only 'yes' will be accepted to approve this step execution.",
			})
			if err != nil {
				c.Ui.Error(err.Error())
				return 1
			}
			if !approved {
				c.Ui.Output("Runbook execution cancelled.")
				return 1
			}
		}
		applyFiles := stripExecuteUnsafeFiles(planFiles)
		if hasApplyableConfig(applyFiles) {
			applyConfig, _, applyConfigDiags := loadLoweredStepConfig(applyFiles, runbookInputs)
			if len(applyConfigDiags) != 0 {
				c.showRemappedDiagnostics(applyConfigDiags, sourceMaps[stepName])
			}
			if applyConfigDiags.HasErrors() {
				return 1
			}
			applyPlan, applyPlanDiags := tfCtx.Plan(applyConfig, stepState, &terraform.PlanOpts{
				Mode:         plans.NormalMode,
				Query:        false,
				SetVariables: inputValues,
			})
			if len(applyPlanDiags) != 0 {
				c.showRemappedDiagnostics(applyPlanDiags, sourceMaps[stepName])
			}
			if applyPlanDiags.HasErrors() {
				return 1
			}
			newState, applyDiags := tfCtx.Apply(applyPlan, applyConfig, &terraform.ApplyOpts{})
			if applyDiags.HasErrors() {
				c.Ui.Error(applyDiags.Err().Error())
				return 1
			}
			if newState != nil {
				stepState = newState
			}
		}

		invokeFiles := planFiles
		if len(manifestStep.PlannedActions) > 0 {
			invokeFiles = stripInvokeUnsafeFiles(planFiles)
		}
		for _, actionRef := range manifestStep.PlannedActions {
			target, targetDiags := runbookActionTarget(actionRefForManifestStep(manifestStep, actionRef))
			if targetDiags.HasErrors() {
				c.Ui.Error(targetDiags.Err().Error())
				return 1
			}
			planConfig, invokeInputValues, invokeConfigDiags := loadLoweredStepConfig(invokeFiles, runbookInputs)
			if len(invokeConfigDiags) != 0 {
				c.showRemappedDiagnostics(invokeConfigDiags, sourceMaps[stepName])
			}
			if invokeConfigDiags.HasErrors() {
				return 1
			}
			invokePlan, invokePlanDiags := tfCtx.Plan(planConfig, states.NewState(), &terraform.PlanOpts{
				Mode:          plans.RefreshOnlyMode,
				ActionTargets: []addrs.Targetable{target},
				SetVariables:  invokeInputValues,
			})
			if len(invokePlanDiags) != 0 {
				c.showRemappedDiagnostics(invokePlanDiags, sourceMaps[stepName])
			}
			if invokePlanDiags.HasErrors() {
				return 1
			}
			if invokePlan == nil || invokePlan.Changes == nil || len(invokePlan.Changes.ActionInvocations) == 0 {
				c.Ui.Error(fmt.Sprintf("runbook execute did not produce an action invocation for %s", actionRef))
				return 1
			}
			c.Ui.Output(c.Colorize().Color(fmt.Sprintf("[bold]Action started: %s[reset]", actionRef)))
			_, invokeApplyDiags := tfCtx.Apply(invokePlan, planConfig, &terraform.ApplyOpts{})
			if invokeApplyDiags.HasErrors() {
				c.Ui.Error(invokeApplyDiags.Err().Error())
				return 1
			}
			c.Ui.Output(c.Colorize().Color(fmt.Sprintf("[bold][green]Action complete: %s[reset]", actionRef)))
		}

		stepOutputs := stepResults.Get(stepInstanceAddrFromManifestStep(manifestStep))
		stepOutputs = mergeStepOutputs(stepOutputsFromState(stepState, step), stepOutputs)
		postStepResults := runbookeval.NewStepResults()
		seedStepResultsFromPlan(manifest, postStepResults)
		for _, priorStepName := range manifest.StepOrder {
			priorManifestStep := persistedRunbookStep(manifest, priorStepName)
			if priorManifestStep == nil {
				continue
			}
			addr := stepInstanceAddrFromManifestStep(priorManifestStep)
			if priorStepName == stepName {
				postStepResults.Set(addr, stepOutputs)
				continue
			}
			postStepResults.Set(addr, stepResults.Get(addr))
		}

		postScope := runbookconfig.EvalScope{
			Variables: varScope,
			Steps:     postStepResults.ScopeValue(),
			Count:     countScopeForManifestStep(manifestStep),
			Each:      eachScopeForManifestStep(manifestStep),
			Actions:   actionOutputHook.ScopeValue(),
			Workspace: mergedWorkspaceScope(baseWorkspaceScope, originalWorkspaceState),
		}
		postEval := runbookconfig.EvaluateStepForExecution(step, preScope, postScope)
		if postEval.Status == runbookconfig.StepStatusFailed {
			if _, _, conditionErr := terraformDrivenStepConditionStatus(&runbookconfig.Step{Postconditions: step.Postconditions}, rootOutputValuesFromState(stepState)); conditionErr == nil {
				postEval = runbookconfig.StepEvaluation{Status: runbookconfig.StepStatusSucceeded, Detail: "step execution conditions satisfied"}
			}
		}
		if postEval.Status == runbookconfig.StepStatusFailed {
			c.Ui.Error(formatRunbookEvalDiagnostics(stepName, postEval))
			return 1
		}

		if len(step.Lists) == 0 {
			stepOutputs = mergeStepOutputs(evaluateStepOutputsFromSource(step, postScope), stepOutputs)
		}
		stepOutputs = mergeStepOutputs(evaluateStepOutputsFromSourceWithExecuteActions(step, postScope), stepOutputs)
		stepResults.Set(stepInstanceAddrFromManifestStep(manifestStep), stepOutputs)
		c.Ui.Output(c.Colorize().Color(fmt.Sprintf("[bold][cyan]# %s[reset]", stepName)))
		c.Ui.Output(c.Colorize().Color("status = [green]\"complete\"[reset]"))
		if formatted := formatActionInvocations(c.Colorize(), manifestStep.PlannedActions); formatted != "" {
			for _, line := range strings.Split(formatted, "\n") {
				c.Ui.Output(line)
			}
		}
		if formatted := formatStepOutputs(c.Colorize(), stepName, stepOutputs); formatted != "" {
			for _, line := range strings.Split(formatted, "\n") {
				c.Ui.Output(line)
			}
		}
		c.Ui.Output("")
	}

	if err := c.writeRunbookStateSnapshot(ctx, manifest.Workspace, originalWorkspaceState); err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to persist runbook state: %s", err))
		return 1
	}

	c.Ui.Output(c.Colorize().Color("[bold][green]Runbook apply complete.[reset]"))
	return 0
}

func (c *RunbookCommand) parseRunbookPlanArgs(args []string) (*runbookPlanArgs, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := &runbookPlanArgs{}
	cmdFlags := c.defaultFlagSet("runbook plan")
	cmdFlags.StringVar(&ret.OutPath, "out", "", "out")
	varsFlags := arguments.NewFlagNameValueSlice("-var")
	varFilesFlags := varsFlags.Alias("-var-file")
	ret.Vars = runbookVarsFromFlags(&varsFlags, &varFilesFlags)
	cmdFlags.Var(&varsFlags, "var", "var")
	cmdFlags.Var(&varFilesFlags, "var-file", "var-file")
	if err := cmdFlags.Parse(args); err != nil {
		diags = diags.Append(tfdiags.Sourceless(tfdiags.Error, "Failed to parse command-line flags", err.Error()))
	}
	if len(cmdFlags.Args()) > 0 {
		diags = diags.Append(tfdiags.Sourceless(tfdiags.Error, "Too many command line arguments", "To save a runbook plan file, use the -out flag."))
	}
	return ret, diags
}

func (c *RunbookCommand) parseRunbookExecuteArgs(args []string) (*runbookExecuteArgs, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := &runbookExecuteArgs{}
	cmdFlags := c.defaultFlagSet("runbook execute")
	cmdFlags.BoolVar(&ret.AutoApprove, "auto-approve", false, "auto-approve")
	cmdFlags.BoolVar(&ret.InputEnabled, "input", true, "input")
	varsFlags := arguments.NewFlagNameValueSlice("-var")
	varFilesFlags := varsFlags.Alias("-var-file")
	ret.Vars = runbookVarsFromFlags(&varsFlags, &varFilesFlags)
	cmdFlags.Var(&varsFlags, "var", "var")
	cmdFlags.Var(&varFilesFlags, "var-file", "var-file")
	if err := cmdFlags.Parse(args); err != nil {
		diags = diags.Append(tfdiags.Sourceless(tfdiags.Error, "Failed to parse command-line flags", err.Error()))
	}
	remaining := cmdFlags.Args()
	if len(remaining) > 1 {
		diags = diags.Append(tfdiags.Sourceless(tfdiags.Error, "Too many command line arguments", "Expected at most one runbook plan file path."))
	} else if len(remaining) == 1 {
		ret.PlanPath = remaining[0]
	}
	return ret, diags
}

func (c *RunbookCommand) showRunbookPlanSummary(manifest *runbookplanfile.Plan) {
	for _, line := range strings.Split(formatPlanSummary(c.Colorize(), manifest), "\n") {
		if line == "" {
			c.Ui.Output("")
			continue
		}
		c.Ui.Output(line)
	}
	c.Ui.Output("")
}

func runbookVarsFromFlags(varsFlags, varFilesFlags *arguments.FlagNameValueSlice) arguments.Vars {
	return arguments.VarsFromFlagSlices(varsFlags, varFilesFlags)
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

func (c *RunbookCommand) loadRunbookVariableValues(cfg *runbookconfig.Config) (cty.Value, terraform.InputValues, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	decls := runbookVariableDecls(cfg)
	collected := c.VariableValues
	if collected == nil {
		var collectDiags tfdiags.Diagnostics
		collected, collectDiags = (&arguments.Vars{}).CollectValues(func(string, []byte) {})
		diags = diags.Append(collectDiags)
	}
	inputValues, parseDiags := backendrun.ParseVariableValues(collected, decls)
	diags = diags.Append(parseDiags)
	return buildRunbookVariableScope(cfg, inputValues), inputValues, diags
}

func runbookVariableDecls(cfg *runbookconfig.Config) map[string]*configs.Variable {
	if cfg == nil || len(cfg.Variables) == 0 {
		return map[string]*configs.Variable{}
	}
	ret := make(map[string]*configs.Variable, len(cfg.Variables))
	for name, variable := range cfg.Variables {
		decl := &configs.Variable{
			Name:           name,
			Type:           cty.DynamicPseudoType,
			ConstraintType: cty.DynamicPseudoType,
			ParsingMode:    configs.VariableParseLiteral,
			Nullable:       true,
			Default:        cty.NilVal,
		}
		if variable != nil && variable.Default != nil {
			if val, defaultDiags := variable.Default.Value(&hcl.EvalContext{}); !defaultDiags.HasErrors() {
				decl.Default = val
			}
		}
		ret[name] = decl
	}
	return ret
}

func buildRunbookVariableScope(cfg *runbookconfig.Config, inputValues terraform.InputValues) cty.Value {
	if cfg == nil || len(cfg.Variables) == 0 {
		return cty.EmptyObjectVal
	}
	vals := make(map[string]cty.Value, len(cfg.Variables))
	for name, variable := range cfg.Variables {
		if input := inputValues[name]; input != nil && input.Value != cty.NilVal {
			vals[name] = input.Value
			continue
		}
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

func runbookActionTarget(ref string) (addrs.Targetable, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if strings.HasPrefix(ref, "workspace.") {
		ref = strings.TrimPrefix(ref, "workspace.")
	}
	if strings.Contains(ref, "[") {
		addr, addrDiags := addrs.ParseAbsActionInstanceStr(ref)
		diags = diags.Append(addrDiags)
		if addrDiags.HasErrors() {
			return nil, diags
		}
		return addr, diags
	}
	addr, addrDiags := addrs.ParseAbsActionStr(ref)
	diags = diags.Append(addrDiags)
	if addrDiags.HasErrors() {
		return nil, diags
	}
	return addr, diags
}

func actionRefForManifestStep(step *runbookplanfile.Step, ref string) string {
	return ref
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
	if diags[0].Detail != "" {
		return diags[0].Detail
	}
	return diags[0].Summary
}

func (c *RunbookCommand) planRunbookStepInstance(stepName string, cfg *runbookconfig.Config, rawStep *runbookconfig.Step, protoStep *runbooks.FindRunbookConfigurationSteps_Step, scope runbookconfig.EvalScope, runbookInputs terraform.InputValues, providerFactories map[addrs.Provider]providers.Factory) (runbookplan.StepPlanResult, error) {
	if rawStep == nil {
		return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
	}
	loweredBundle, lowerDiags := runbookconfig.LowerStepInstanceWithScope(cfg, rawStep, scope)
	if len(lowerDiags) != 0 {
		c.showDiagnostics(lowerDiags)
		if lowerDiags.HasErrors() {
			return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
		}
	}
	if loweredBundle == nil {
		return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
	}
	planConfig, inputValues, configDiags := loadLoweredStepConfig(loweredBundle.Files, runbookInputs)
	if len(configDiags) != 0 {
		c.showDiagnostics(configDiags)
		if configDiags.HasErrors() {
			return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
		}
	}
	hasQueries := hasLoweredQueryFiles(loweredBundle.Files)
	tfCtx, ctxDiags := terraform.NewContext(&terraform.ContextOpts{Parallelism: 1, Providers: providerFactories})
	if len(ctxDiags) != 0 {
		c.showDiagnostics(ctxDiags)
		if ctxDiags.HasErrors() {
			return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
		}
	}
	validateDiags := tfCtx.Validate(planConfig, &terraform.ValidateOpts{Query: hasQueries})
	if len(validateDiags) != 0 {
		c.showDiagnostics(validateDiags)
		if validateDiags.HasErrors() {
			return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
		}
	}
	tfPlan, planDiags := tfCtx.Plan(planConfig, states.NewState(), &terraform.PlanOpts{
		Mode:         plans.NormalMode,
		Query:        hasQueries,
		SetVariables: inputValues,
	})
	if len(planDiags) != 0 {
		c.showDiagnostics(planDiags)
		if planDiags.HasErrors() {
			return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
		}
	}
	plannedOutputs := map[string]cty.Value{}
	queries := []runbookconfig.PlannedQuery{}
	if tfPlan != nil && tfPlan.Changes != nil {
		schemas, schemaDiags := tfCtx.Schemas(planConfig, states.NewState())
		if len(schemaDiags) != 0 {
			c.showDiagnostics(schemaDiags)
			if schemaDiags.HasErrors() {
				return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
			}
		}
		for _, q := range tfPlan.Changes.Queries {
			if q == nil {
				continue
			}
			count := 0
			data := cty.NilVal
			if schema := schemaForLocalPlannedQuery(schemas, q); schema != nil {
				if decoded, err := q.Decode(*schema); err == nil && decoded.Results.Value != cty.NilVal && decoded.Results.Value.Type().HasAttribute("data") {
					decodedData := decoded.Results.Value.GetAttr("data")
					decodedData, _ = decodedData.UnmarkDeep()
					data = decodedData
					if data.IsKnown() && !data.IsNull() && (data.Type().IsTupleType() || data.Type().IsListType()) {
						count = data.LengthInt()
					}
				}
			}
			queries = append(queries, runbookconfig.PlannedQuery{Address: q.Addr.String(), Count: count, Data: data})
		}
		for _, output := range tfPlan.Changes.Outputs {
			if output == nil {
				continue
			}
			decoded, err := output.Decode()
			if err != nil {
				c.Ui.Error(fmt.Sprintf("Failed to decode lowered step output: %s", err))
				return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
			}
			plannedOutputs[decoded.Addr.OutputValue.Name] = decoded.Change.After
		}
	}
	plannedOutputs = mergeOutputMaps(plannedOutputs, outputsMapFromValue(runbookplan.StepOutputsFromQueriesWithScope(rawStep, queries, scope)))
	knownSkipped, skipReason, conditionErr := terraformDrivenStepConditionStatus(rawStep, plannedOutputs)
	if conditionErr != nil {
		c.Ui.Error(conditionErr.Error())
		return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
	}
	invokeFiles := loweredBundle.Files
	if len(rawStep.ExecuteInvokes) > 0 {
		invokeFiles = stripInvokeUnsafeFiles(loweredBundle.Files)
	}
	for _, actionRef := range staticActionAddresses(cfg, rawStep) {
		target, targetDiags := runbookActionTarget(actionRef)
		if len(targetDiags) != 0 {
			c.showDiagnostics(targetDiags)
			if targetDiags.HasErrors() {
				return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
			}
		}
		invokeConfig, invokeInputs, invokeConfigDiags := loadLoweredStepConfig(invokeFiles, runbookInputs)
		if len(invokeConfigDiags) != 0 {
			c.showDiagnostics(invokeConfigDiags)
			if invokeConfigDiags.HasErrors() {
				return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
			}
		}
		invokePlan, invokePlanDiags := tfCtx.Plan(invokeConfig, states.NewState(), &terraform.PlanOpts{
			Mode:          plans.RefreshOnlyMode,
			ActionTargets: []addrs.Targetable{target},
			SetVariables:  invokeInputs,
		})
		if len(invokePlanDiags) != 0 {
			c.showDiagnostics(invokePlanDiags)
			if invokePlanDiags.HasErrors() {
				return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
			}
		}
		if invokePlan == nil || invokePlan.Changes == nil || len(invokePlan.Changes.ActionInvocations) == 0 {
			c.Ui.Error(fmt.Sprintf("Failed to plan runbook action invocation: Runbook action %s did not produce an action invocation during planning.", actionRef))
			return runbookplan.StepPlanResult{}, runbookStepPlanningFailedError{stepName: stepName}
		}
	}
	outputsVal := valueFromOutputMap(plannedOutputs)
	return runbookplan.StepPlanResult{
		Outputs:        outputsVal,
		Queries:        queries,
		KnownSkipped:   knownSkipped,
		SkipReason:     skipReason,
		PlannedActions: staticActionAddresses(cfg, rawStep),
		PlannedQueries: staticQueryAddresses(rawStep),
		PlannedData:    staticPlannedData(rawStep),
		OutputNames:    protoStep.Outputs,
		LoweredFiles:   loweredBundle.Files,
		SourceMaps:     loweredBundle.SourceMaps,
	}, nil
}

func schemaForLocalPlannedQuery(schemas *terraform.Schemas, q *plans.QueryInstanceSrc) *providers.Schema {
	if schemas == nil || q == nil {
		return nil
	}
	providerSchema, ok := schemas.Providers[q.ProviderAddr.Provider]
	if !ok {
		return nil
	}
	schema, ok := providerSchema.ListResourceTypes[q.Addr.Resource.Resource.Type]
	if !ok {
		return nil
	}
	return &schema
}

func terraformDrivenStepConditionStatus(step *runbookconfig.Step, outputs map[string]cty.Value) (bool, string, error) {
	if step == nil {
		return false, "", nil
	}
	check := func(conds []*runbookconfig.Condition, kind string, allowSkip bool) (bool, string, error) {
		for i, cond := range conds {
			if cond == nil {
				continue
			}
			result, ok := outputs[fmt.Sprintf("__runbook_%s_%d_condition", kind, i)]
			if !ok || !result.IsKnown() || result.IsNull() || result.Type() != cty.Bool || result.True() {
				continue
			}
			message := "A step condition returned false."
			if msg, ok := outputs[fmt.Sprintf("__runbook_%s_%d_error_message", kind, i)]; ok && msg.IsKnown() && !msg.IsNull() && msg.Type() == cty.String {
				message = msg.AsString()
			}
			if allowSkip && cond.OnFail == runbookconfig.ConditionOnFailSkip {
				return true, message, nil
			}
			return false, "", fmt.Errorf("Runbook condition failed: %s", message)
		}
		return false, "", nil
	}
	if skipped, reason, err := check(step.Preconditions, "precondition", true); skipped || err != nil {
		return skipped, reason, err
	}
	_, _, err := check(step.Postconditions, "postcondition", false)
	return false, "", err
}

func outputsMapFromValue(v cty.Value) map[string]cty.Value {
	if v == cty.NilVal || !v.Type().IsObjectType() {
		return nil
	}
	return v.AsValueMap()
}

func valueFromOutputMap(vals map[string]cty.Value) cty.Value {
	if len(vals) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(vals)
}

func mergeOutputMaps(primary, fallback map[string]cty.Value) map[string]cty.Value {
	ret := map[string]cty.Value{}
	for name, val := range fallback {
		ret[name] = val
	}
	for name, val := range primary {
		ret[name] = val
	}
	return ret
}

func (c *RunbookCommand) showRunbookProtoDiagnostics(diags []*terraform1.Diagnostic) {
	for _, diag := range diags {
		if diag == nil {
			continue
		}
		msg := diag.GetSummary()
		if diag.GetDetail() != "" {
			msg = fmt.Sprintf("%s: %s", diag.GetSummary(), diag.GetDetail())
		}
		if loc := formatProtoDiagnosticLocation(diag); loc != "" {
			msg = fmt.Sprintf("%s (%s)", msg, loc)
		}
		switch diag.GetSeverity() {
		case terraform1.Diagnostic_ERROR:
			c.Ui.Error(msg)
		case terraform1.Diagnostic_WARNING:
			c.Ui.Warn(msg)
		default:
			c.Ui.Output(msg)
		}
	}
}

func formatProtoDiagnosticLocation(diag *terraform1.Diagnostic) string {
	if diag == nil {
		return ""
	}
	rng := diag.GetSubject()
	if rng == nil {
		rng = diag.GetContext()
	}
	if rng == nil || rng.GetSourceAddr() == "" {
		return ""
	}
	if isGeneratedRunbookSourceAddr(rng.GetSourceAddr()) {
		return ""
	}
	start := rng.GetStart()
	if start == nil || start.GetLine() <= 0 {
		return rng.GetSourceAddr()
	}
	if start.GetColumn() <= 0 {
		return fmt.Sprintf("%s:%d", rng.GetSourceAddr(), start.GetLine())
	}
	return fmt.Sprintf("%s:%d:%d", rng.GetSourceAddr(), start.GetLine(), start.GetColumn())
}

func isGeneratedRunbookSourceAddr(sourceAddr string) bool {
	if sourceAddr == "" {
		return false
	}
	return strings.Contains(sourceAddr, "terraform-runbook-step-") || strings.Contains(sourceAddr, "terraform-runbook-execute-")
}

func collectManifestSourceMaps(plan *runbookplanfile.Plan) map[string]map[string][]runbookconfig.SourceMapEntry {
	if plan == nil {
		return nil
	}
	ret := map[string]map[string][]runbookconfig.SourceMapEntry{}
	for i := range plan.Steps {
		step := &plan.Steps[i]
		if step == nil || len(step.SourceMaps) == 0 {
			continue
		}
		ret[step.Name] = decodePlanfileSourceMaps(step.SourceMaps)
	}
	if len(ret) == 0 {
		return nil
	}
	return ret
}

func decodePlanfileSourceMaps(sourceMaps map[string][]runbookplanfile.RunbookSourceMapEntry) map[string][]runbookconfig.SourceMapEntry {
	if len(sourceMaps) == 0 {
		return nil
	}
	ret := make(map[string][]runbookconfig.SourceMapEntry, len(sourceMaps))
	for name, entries := range sourceMaps {
		mapped := make([]runbookconfig.SourceMapEntry, 0, len(entries))
		for _, entry := range entries {
			mapped = append(mapped, runbookconfig.SourceMapEntry{
				GeneratedStartLine: entry.GeneratedStartLine,
				GeneratedEndLine:   entry.GeneratedEndLine,
				OriginalRange: tfdiags.SourceRange{
					Filename: entry.OriginalRange.Filename,
					Start:    tfdiags.SourcePos{Line: entry.OriginalRange.Start.Line, Column: entry.OriginalRange.Start.Column, Byte: entry.OriginalRange.Start.Byte},
					End:      tfdiags.SourcePos{Line: entry.OriginalRange.End.Line, Column: entry.OriginalRange.End.Column, Byte: entry.OriginalRange.End.Byte},
				},
			})
		}
		ret[name] = mapped
	}
	return ret
}

func (c *RunbookCommand) showRemappedDiagnostics(diags tfdiags.Diagnostics, sourceMaps map[string][]runbookconfig.SourceMapEntry) {
	if len(diags) == 0 {
		return
	}
	if len(sourceMaps) == 0 {
		c.showDiagnostics(diags)
		return
	}
	c.showDiagnostics(remapDiagnosticsToRunbookSources(diags, sourceMaps))
}

func remapDiagnosticsToRunbookSources(diags tfdiags.Diagnostics, sourceMaps map[string][]runbookconfig.SourceMapEntry) tfdiags.Diagnostics {
	if len(diags) == 0 || len(sourceMaps) == 0 {
		return diags
	}
	ret := make(tfdiags.Diagnostics, 0, len(diags))
	for _, diag := range diags {
		ret = append(ret, remapDiagnosticToRunbookSources(diag, sourceMaps))
	}
	return ret
}

func remapDiagnosticToRunbookSources(diag tfdiags.Diagnostic, sourceMaps map[string][]runbookconfig.SourceMapEntry) tfdiags.Diagnostic {
	if diag == nil {
		return diag
	}
	src := diag.Source()
	changed := false
	if mapped := remapSourceRange(src.Subject, sourceMaps); mapped != nil {
		src.Subject = mapped
		changed = true
	}
	if mapped := remapSourceRange(src.Context, sourceMaps); mapped != nil {
		src.Context = mapped
		changed = true
	}
	if !changed {
		return diag
	}
	return &runbookMappedDiagnostic{Diagnostic: diag, source: src}
}

func remapSourceRange(rng *tfdiags.SourceRange, sourceMaps map[string][]runbookconfig.SourceMapEntry) *tfdiags.SourceRange {
	if rng == nil || !isGeneratedRunbookSourceAddr(rng.Filename) {
		return nil
	}
	entries := sourceMaps[filepath.Base(rng.Filename)]
	if len(entries) == 0 {
		for name, candidate := range sourceMaps {
			if name == filepath.Base(rng.Filename) {
				entries = candidate
				break
			}
		}
	}
	if len(entries) == 0 {
		return nil
	}
	line := rng.Start.Line
	var nearest *runbookconfig.SourceMapEntry
	for _, entry := range entries {
		if line < entry.GeneratedStartLine || line > entry.GeneratedEndLine {
			if line >= entry.GeneratedStartLine {
				entryCopy := entry
				nearest = &entryCopy
			}
			continue
		}
		mapped := entry.OriginalRange
		return &mapped
	}
	if nearest != nil {
		mapped := nearest.OriginalRange
		return &mapped
	}
	return nil
}

type runbookMappedDiagnostic struct {
	tfdiags.Diagnostic
	source tfdiags.Source
}

func (d *runbookMappedDiagnostic) Source() tfdiags.Source {
	return d.source
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

func loadLoweredStepConfig(files map[string][]byte, provided terraform.InputValues) (*configs.Config, terraform.InputValues, tfdiags.Diagnostics) {
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
	inputValues := rootModuleInputValues(mod.Variables, provided)
	config, buildDiags := terraform.BuildConfigWithGraph(mod, loader.ModuleWalker(), inputValues, configs.MockDataLoaderFunc(loader.LoadExternalMockData))
	diags = diags.Append(buildDiags)
	return config, inputValues, diags
}

func rootModuleInputValues(decls map[string]*configs.Variable, provided terraform.InputValues) terraform.InputValues {
	ret := make(terraform.InputValues, len(decls))
	for name := range decls {
		if provided != nil {
			if value, ok := provided[name]; ok && value != nil {
				ret[name] = value
				continue
			}
		}
		ret[name] = &terraform.InputValue{Value: cty.NilVal, SourceType: terraform.ValueFromCaller}
	}
	return ret
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
		body.RemoveBlock(block)
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
		if runbookconfig.ExprReferencesRunbookActionOutput(output.Value) {
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

func evaluateStepOutputsFromSourceWithExecuteActions(step *runbookconfig.Step, scope runbookconfig.EvalScope) cty.Value {
	if step == nil || len(step.Outputs) == 0 {
		return cty.EmptyObjectVal
	}
	scope = runbookconfig.ScopeWithStepLists(step, scope)
	vals := make(map[string]cty.Value, len(step.Outputs))
	for name, output := range step.Outputs {
		if output == nil || output.Value == nil {
			continue
		}
		if !runbookconfig.ExprReferencesRunbookActionOutput(output.Value) {
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

func stripOutputBlocks(files map[string][]byte, names []string) map[string][]byte {
	if len(names) == 0 {
		return files
	}
	mainSrc, ok := files["main.tf"]
	if !ok {
		return files
	}
	parsed, diags := hclwrite.ParseConfig(mainSrc, "main.tf", hcl.InitialPos)
	if diags.HasErrors() || parsed == nil {
		return files
	}
	remove := map[string]struct{}{}
	for _, name := range names {
		remove[name] = struct{}{}
	}
	body := parsed.Body()
	for _, block := range body.Blocks() {
		if block.Type() != "output" {
			continue
		}
		labels := block.Labels()
		if len(labels) != 1 {
			continue
		}
		if _, ok := remove[labels[0]]; ok {
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

func executeTimeActionOutputNames(step *runbookconfig.Step) []string {
	if step == nil || len(step.Outputs) == 0 {
		return nil
	}
	ret := make([]string, 0)
	for name, output := range step.Outputs {
		if output == nil || output.Value == nil {
			continue
		}
		if runbookconfig.ExprReferencesRunbookActionOutput(output.Value) {
			ret = append(ret, name)
		}
	}
	return ret
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

func stripInvokeUnsafeFiles(files map[string][]byte) map[string][]byte {
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
		if block.Type() == "output" {
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
