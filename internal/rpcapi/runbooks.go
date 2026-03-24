// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package rpcapi

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-svchost/disco"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configload"
	"github.com/hashicorp/terraform/internal/depsfile"
	"github.com/hashicorp/terraform/internal/plans"
	"github.com/hashicorp/terraform/internal/providercache"
	"github.com/hashicorp/terraform/internal/providers"
	"github.com/zclconf/go-cty/cty"
	ctymsgpack "github.com/zclconf/go-cty/cty/msgpack"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/runbooks"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/runbooks/runbookplan"
	"github.com/hashicorp/terraform/internal/states"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type runbooksServer struct {
	runbooks.UnimplementedRunbooksServer

	handles  *handleTable
	services *disco.Disco

	providerCacheOverride map[addrs.Provider]providers.Factory
}

func newRunbooksServer(handles *handleTable, services *disco.Disco) *runbooksServer {
	return &runbooksServer{handles: handles, services: services}
}

func (s *runbooksServer) OpenRunbookConfiguration(ctx context.Context, req *runbooks.OpenRunbookConfiguration_Request) (*runbooks.OpenRunbookConfiguration_Response, error) {
	config, diags := runbookconfig.LoadConfigDir(req.ConfigPath)
	if diags.HasErrors() {
		return &runbooks.OpenRunbookConfiguration_Response{
			Diagnostics: diagnosticsToProto(diags),
		}, nil
	}

	configHnd := s.handles.NewRunbookConfig(config)

	return &runbooks.OpenRunbookConfiguration_Response{
		RunbookConfigHandle: configHnd.ForProtobuf(),
		Diagnostics:         diagnosticsToProto(diags),
	}, nil
}

func (s *runbooksServer) OpenRunbookRuntime(ctx context.Context, req *runbooks.OpenRunbookRuntime_Request) (*runbooks.OpenRunbookRuntime_Response, error) {
	locks := s.handles.DependencyLocks(handle[*depsfile.Locks](req.DependencyLocksHandle))
	if locks == nil {
		return nil, status.Error(codes.InvalidArgument, "the given dependency locks handle is invalid")
	}
	cache := s.handles.ProviderPluginCache(handle[*providercache.Dir](req.ProviderCacheHandle))
	if cache == nil {
		return nil, status.Error(codes.InvalidArgument, "the given provider cache handle is invalid")
	}
	runtimeHnd := s.handles.NewRunbookRuntime(&runbookRuntime{Locks: locks, ProviderCache: cache})
	return &runbooks.OpenRunbookRuntime_Response{RunbookRuntimeHandle: runtimeHnd.ForProtobuf()}, nil
}

func (s *runbooksServer) CloseRunbookRuntime(ctx context.Context, req *runbooks.CloseRunbookRuntime_Request) (*runbooks.CloseRunbookRuntime_Response, error) {
	hnd := handle[*runbookRuntime](req.RunbookRuntimeHandle)
	if err := s.handles.CloseRunbookRuntime(hnd); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &runbooks.CloseRunbookRuntime_Response{}, nil
}

func (s *runbooksServer) CloseRunbookConfiguration(ctx context.Context, req *runbooks.CloseRunbookConfiguration_Request) (*runbooks.CloseRunbookConfiguration_Response, error) {
	hnd := handle[*runbookconfig.Config](req.RunbookConfigHandle)
	err := s.handles.CloseRunbookConfig(hnd)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &runbooks.CloseRunbookConfiguration_Response{}, nil
}

func (s *runbooksServer) ValidateRunbookConfiguration(ctx context.Context, req *runbooks.ValidateRunbookConfiguration_Request) (*runbooks.ValidateRunbookConfiguration_Response, error) {
	cfgHnd := handle[*runbookconfig.Config](req.RunbookConfigHandle)
	cfg := s.handles.RunbookConfig(cfgHnd)
	if cfg == nil {
		return nil, status.Error(codes.InvalidArgument, "the given runbook configuration handle is invalid")
	}

	diags := runbookconfig.Validate(cfg)

	return &runbooks.ValidateRunbookConfiguration_Response{
		Diagnostics: diagnosticsToProto(diags),
	}, nil
}

func (s *runbooksServer) FindRunbookConfigurationSteps(ctx context.Context, req *runbooks.FindRunbookConfigurationSteps_Request) (*runbooks.FindRunbookConfigurationSteps_Response, error) {
	cfgHnd := handle[*runbookconfig.Config](req.RunbookConfigHandle)
	cfg := s.handles.RunbookConfig(cfgHnd)
	if cfg == nil {
		return nil, status.Error(codes.InvalidArgument, "the given runbook configuration handle is invalid")
	}

	return &runbooks.FindRunbookConfigurationSteps_Response{
		Config: runbookConfigToProto(cfg),
	}, nil
}

func (s *runbooksServer) PlanRunbookStep(ctx context.Context, req *runbooks.PlanRunbookStep_Request) (*runbooks.PlanRunbookStep_Response, error) {
	step, err := s.lookupStep(req.RunbookConfigHandle, req.StepName)
	if err != nil {
		return nil, err
	}

	scope, diags := evalScopeFromProto(req.Scope)
	if diags.HasErrors() {
		return &runbooks.PlanRunbookStep_Response{
			Status:      runbooks.StepStatus_STEP_STATUS_FAILED,
			Detail:      "invalid plan scope",
			Diagnostics: diagnosticsToProto(diags),
		}, nil
	}

	cfg := s.handles.RunbookConfig(handle[*runbookconfig.Config](req.RunbookConfigHandle))
	plan := runbookconfig.PlanStepWithConfig(cfg, step, scope)
	plannedOutputVals := plannedOutputsFromStepPlan(plan, step)
	if plan.Lowered != nil {
		runtime, err := s.lookupRunbookRuntime(req.RunbookRuntimeHandle)
		if err != nil {
			return nil, err
		}
		tfPlan, lowerDiags := s.validateAndPlanLoweredStepDir(plan.Lowered.Dir, runtime)
		plan.Evaluation.Diags = plan.Evaluation.Diags.Append(lowerDiags)
		if tfPlan != nil && tfPlan.Changes != nil {
			schemas, schemaDiags := s.schemasForRunbookPlan(plan.Lowered.Dir, runtime)
			plan.Evaluation.Diags = plan.Evaluation.Diags.Append(schemaDiags)
			for _, q := range tfPlan.Changes.Queries {
				count := int64(0)
				data := cty.NilVal
				if q != nil {
					schema := schemaForPlannedQuery(schemas, q)
					if schema != nil {
						if decoded, err := q.Decode(*schema); err == nil && decoded.Results.Value != cty.NilVal && decoded.Results.Value.Type().HasAttribute("data") {
							decodedData := decoded.Results.Value.GetAttr("data")
							decodedData, _ = decodedData.UnmarkDeep()
							data = decodedData
							if data.IsKnown() && !data.IsNull() && (data.Type().IsTupleType() || data.Type().IsListType()) {
								count = int64(data.LengthInt())
							}
						}
					}
				}
				plan.Queries = append(plan.Queries, runbookconfig.PlannedQuery{
					Address: q.Addr.String(),
					Count:   int(count),
					Data:    data,
				})
			}
			for _, action := range tfPlan.Changes.ActionInvocations {
				if action == nil {
					continue
				}
				plan.Actions = append(plan.Actions, runbookconfig.PlannedAction{
					Address:    action.Addr.String(),
					ActionType: action.Addr.Action.Action.Type,
					ActionName: action.Addr.Action.Action.Name,
				})
			}
			for _, output := range tfPlan.Changes.Outputs {
				if output == nil {
					continue
				}
				decoded, err := output.Decode()
				if err != nil {
					plan.Evaluation.Diags = plan.Evaluation.Diags.Append(tfdiags.Sourceless(tfdiags.Error, "Failed to decode lowered step output", err.Error()))
					continue
				}
				plannedOutputVals[decoded.Addr.OutputValue.Name] = decoded.Change.After
			}
			for name, output := range runbookplan.StepOutputsFromQueries(step, plan.Queries, scope.Variables, scope.Steps, scope.Workspace).AsValueMap() {
				plannedOutputVals[name] = output
			}
			applyTerraformDrivenStepResults(plan, step, plannedOutputVals)
		}
	}
	plannedActions := make([]*runbooks.PlanRunbookStep_PlannedAction, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		plannedActions = append(plannedActions, &runbooks.PlanRunbookStep_PlannedAction{
			Address:    action.Address,
			ActionType: action.ActionType,
			ActionName: action.ActionName,
		})
	}
	plannedQueries := make([]*runbooks.PlanRunbookStep_PlannedQuery, 0, len(plan.Queries))
	for _, query := range plan.Queries {
		plannedQueries = append(plannedQueries, &runbooks.PlanRunbookStep_PlannedQuery{
			Address:     query.Address,
			ResultCount: int64(query.Count),
			Data:        dynamicValueToProto(query.Data),
		})
	}
	plannedOutputs := make(map[string]*runbooks.DynamicValue)
	for name, output := range plannedOutputVals {
		plannedOutputs[name] = dynamicValueToProto(output)
	}
	loweredFiles := make([]*runbooks.PlanRunbookStep_LoweredFile, 0)
	if plan.Lowered != nil {
		for path, content := range plan.Lowered.Files {
			loweredFiles = append(loweredFiles, &runbooks.PlanRunbookStep_LoweredFile{Path: path, Content: content})
		}
	}
	return &runbooks.PlanRunbookStep_Response{
		Status:         stepStatusToProto(plan.Evaluation.Status),
		Detail:         plan.Evaluation.Detail,
		Diagnostics:    diagnosticsToProto(plan.Evaluation.Diags),
		PlannedActions: plannedActions,
		LoweredFiles:   loweredFiles,
		PlannedQueries: plannedQueries,
		PlannedOutputs: plannedOutputs,
	}, nil
}

func (s *runbooksServer) ExecuteRunbookStep(ctx context.Context, req *runbooks.ExecuteRunbookStep_Request) (*runbooks.ExecuteRunbookStep_Response, error) {
	step, err := s.lookupStep(req.RunbookConfigHandle, req.StepName)
	if err != nil {
		return nil, err
	}

	preScope, preDiags := evalScopeFromProto(req.PreExecuteScope)
	postScope, postDiags := evalScopeFromProto(req.PostExecuteScope)
	allDiags := preDiags.Append(postDiags)
	if allDiags.HasErrors() {
		return &runbooks.ExecuteRunbookStep_Response{
			Status:      runbooks.StepStatus_STEP_STATUS_FAILED,
			Detail:      "invalid execute scope",
			Diagnostics: diagnosticsToProto(allDiags),
		}, nil
	}

	result := runbookconfig.EvaluateStepForExecution(step, preScope, postScope)
	return &runbooks.ExecuteRunbookStep_Response{
		Status:      stepStatusToProto(result.Status),
		Detail:      result.Detail,
		Diagnostics: diagnosticsToProto(result.Diags),
	}, nil
}

func (s *runbooksServer) GetRunnableRunbookSteps(ctx context.Context, req *runbooks.GetRunnableRunbookSteps_Request) (*runbooks.GetRunnableRunbookSteps_Response, error) {
	cfgHnd := handle[*runbookconfig.Config](req.RunbookConfigHandle)
	cfg := s.handles.RunbookConfig(cfgHnd)
	if cfg == nil {
		return nil, status.Error(codes.InvalidArgument, "the given runbook configuration handle is invalid")
	}

	completed := make(map[string]bool, len(req.CompletedSteps))
	for _, step := range req.CompletedSteps {
		completed[step] = true
	}

	runnable, diags := runbookconfig.RunnableSteps(cfg, completed)
	return &runbooks.GetRunnableRunbookSteps_Response{
		StepNames:   runnable,
		Diagnostics: diagnosticsToProto(diags),
	}, nil
}

func runbookConfigToProto(cfg *runbookconfig.Config) *runbooks.FindRunbookConfigurationSteps_RunbookConfig {
	ret := &runbooks.FindRunbookConfigurationSteps_RunbookConfig{
		SourceAddr: cfg.RootPath,
		Steps:      make(map[string]*runbooks.FindRunbookConfigurationSteps_Step),
	}

	if cfg.Runbook != nil {
		ret.TerraformVersion = cfg.Runbook.TerraformVersion
		for _, provider := range cfg.Runbook.RequiredProviders {
			if provider == nil {
				continue
			}
			ret.Providers = append(ret.Providers, &runbooks.FindRunbookConfigurationSteps_ProviderConfig{
				Type: provider.Name,
			})
		}
		for _, variable := range cfg.Variables {
			ret.Variables = append(ret.Variables, &runbooks.FindRunbookConfigurationSteps_Variable{
				Name:       variable.Name,
				HasDefault: variable.Default != nil,
			})
		}
	}

	for _, file := range cfg.Files {
		for name, step := range file.Steps {
			ret.Steps[name] = runbookStepToProto(step)
		}
	}

	return ret
}

func runbookStepToProto(step *runbookconfig.Step) *runbooks.FindRunbookConfigurationSteps_Step {
	ret := &runbooks.FindRunbookConfigurationSteps_Step{
		HasConfig:    step.HasConfig,
		ActionCount:  int64(step.ActionCount),
		ListCount:    int64(step.ListCount),
		ExecuteCount: int64(step.ExecCount),
	}

	for _, condition := range step.Preconditions {
		ret.Preconditions = append(ret.Preconditions, runbookConditionToProto(condition))
	}
	for _, condition := range step.Postconditions {
		ret.Postconditions = append(ret.Postconditions, runbookConditionToProto(condition))
	}
	for name := range step.Locals {
		ret.Locals = append(ret.Locals, name)
	}
	for name := range step.Outputs {
		ret.Outputs = append(ret.Outputs, name)
	}

	return ret
}

func runbookConditionToProto(condition *runbookconfig.Condition) *runbooks.FindRunbookConfigurationSteps_Condition {
	ret := &runbooks.FindRunbookConfigurationSteps_Condition{}
	if condition == nil {
		return ret
	}

	switch condition.OnFail {
	case runbookconfig.ConditionOnFailError:
		ret.OnFail = runbooks.FindRunbookConfigurationSteps_Condition_ON_FAIL_ERROR
	case runbookconfig.ConditionOnFailSkip:
		ret.OnFail = runbooks.FindRunbookConfigurationSteps_Condition_ON_FAIL_SKIP
	default:
		ret.OnFail = runbooks.FindRunbookConfigurationSteps_Condition_ON_FAIL_INVALID
	}

	return ret
}

func (s *runbooksServer) lookupStep(configHandle int64, stepName string) (*runbookconfig.Step, error) {
	cfgHnd := handle[*runbookconfig.Config](configHandle)
	cfg := s.handles.RunbookConfig(cfgHnd)
	if cfg == nil {
		return nil, status.Error(codes.InvalidArgument, "the given runbook configuration handle is invalid")
	}
	for _, file := range cfg.Files {
		if step, ok := file.Steps[stepName]; ok {
			return step, nil
		}
	}
	return nil, status.Errorf(codes.InvalidArgument, "step %q not found", stepName)
}

func evalScopeFromProto(protoScope *runbooks.EvalScope) (runbookconfig.EvalScope, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if protoScope == nil {
		return runbookconfig.EvalScope{}, diags
	}

	variables, moreDiags := dynamicValueFromProto(protoScope.Variables)
	diags = diags.Append(moreDiags)
	list, moreDiags := dynamicValueFromProto(protoScope.List)
	diags = diags.Append(moreDiags)
	actions, moreDiags := dynamicValueFromProto(protoScope.Actions)
	diags = diags.Append(moreDiags)
	steps, moreDiags := dynamicValueFromProto(protoScope.Steps)
	diags = diags.Append(moreDiags)
	locals, moreDiags := dynamicValueFromProto(protoScope.Locals)
	diags = diags.Append(moreDiags)
	workspace, moreDiags := dynamicValueFromProto(protoScope.Workspace)
	diags = diags.Append(moreDiags)
	each, moreDiags := dynamicValueFromProto(protoScope.GetEach())
	diags = diags.Append(moreDiags)

	return runbookconfig.EvalScope{
		Variables: variables,
		List:      list,
		Actions:   actions,
		Steps:     steps,
		Locals:    locals,
		Each:      each,
		Workspace: workspace,
	}, diags
}

func dynamicValueFromProto(protoVal *runbooks.DynamicValue) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if protoVal == nil || len(protoVal.Msgpack) == 0 {
		return cty.EmptyObjectVal, diags
	}

	v, err := ctymsgpack.Unmarshal(protoVal.Msgpack, cty.DynamicPseudoType)
	if err != nil {
		return cty.DynamicVal, diags.Append(status.Errorf(codes.InvalidArgument, "invalid dynamic value encoding: %s", err))
	}
	return v, diags
}

func dynamicValueToProto(v cty.Value) *runbooks.DynamicValue {
	if v == cty.NilVal {
		return nil
	}
	encoded, err := ctymsgpack.Marshal(v, cty.DynamicPseudoType)
	if err != nil {
		return nil
	}
	return &runbooks.DynamicValue{Msgpack: encoded}
}

func plannedOutputsFromStepPlan(plan runbookconfig.StepPlan, step *runbookconfig.Step) map[string]cty.Value {
	ret := make(map[string]cty.Value)
	if step == nil {
		return ret
	}
	for _, query := range plan.Queries {
		if len(step.Outputs) == 1 && query.Data != cty.NilVal {
			for name := range step.Outputs {
				ret[name] = query.Data
			}
		}
	}
	for name := range step.Outputs {
		if _, ok := ret[name]; !ok {
			ret[name] = cty.NullVal(cty.DynamicPseudoType)
		}
	}
	return ret
}

func applyTerraformDrivenStepResults(plan runbookconfig.StepPlan, step *runbookconfig.Step, outputs map[string]cty.Value) {
	if step == nil || len(outputs) == 0 {
		return
	}
	applyTerraformDrivenConditions(&plan.Evaluation, step.Preconditions, "precondition", outputs, true)
	applyTerraformDrivenConditions(&plan.Evaluation, step.Postconditions, "postcondition", outputs, false)
}

func applyTerraformDrivenConditions(eval *runbookconfig.StepEvaluation, conds []*runbookconfig.Condition, kind string, outputs map[string]cty.Value, allowSkip bool) {
	if eval == nil || len(conds) == 0 {
		return
	}
	for i, cond := range conds {
		if cond == nil {
			continue
		}
		result, ok := outputs[fmt.Sprintf("__runbook_%s_%d_condition", kind, i)]
		if !ok || !result.IsKnown() || result.IsNull() || result.Type() != cty.Bool {
			continue
		}
		if result.True() {
			continue
		}
		message := "A step condition returned false."
		if msg, ok := outputs[fmt.Sprintf("__runbook_%s_%d_error_message", kind, i)]; ok && msg.IsKnown() && !msg.IsNull() && msg.Type() == cty.String {
			message = msg.AsString()
		}
		eval.Diags = tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(tfdiags.Error, "Runbook condition failed", message))
		if allowSkip && cond.OnFail == runbookconfig.ConditionOnFailSkip {
			eval.Status = runbookconfig.StepStatusSkipped
			eval.Detail = "step skipped by precondition"
		} else {
			eval.Status = runbookconfig.StepStatusFailed
			eval.Detail = "step condition failed"
		}
		return
	}
}

func stepStatusToProto(status runbookconfig.StepStatus) runbooks.StepStatus {
	switch status {
	case runbookconfig.StepStatusReady:
		return runbooks.StepStatus_STEP_STATUS_READY
	case runbookconfig.StepStatusSkipped:
		return runbooks.StepStatus_STEP_STATUS_SKIPPED
	case runbookconfig.StepStatusFailed:
		return runbooks.StepStatus_STEP_STATUS_FAILED
	case runbookconfig.StepStatusSucceeded:
		return runbooks.StepStatus_STEP_STATUS_SUCCEEDED
	default:
		return runbooks.StepStatus_STEP_STATUS_INVALID
	}
}

func (s *runbooksServer) validateAndPlanLoweredStepDir(dir string, runtime *runbookRuntime) (*plans.Plan, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	loader, err := configload.NewLoader(&configload.Config{
		ModulesDir:        dir + "/.terraform/modules",
		IncludeQueryFiles: true,
	})
	if err != nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Failed to initialize lowered step loader", err.Error()))
	}
	rootMod, hclDiags := loader.LoadRootModule(dir)
	diags = diags.Append(hclDiags)
	if rootMod == nil || hclDiags.HasErrors() {
		return nil, diags
	}
	inputValues := make(terraform.InputValues)
	for name := range rootMod.Variables {
		inputValues[name] = &terraform.InputValue{Value: cty.NilVal, SourceType: terraform.ValueFromCaller}
	}
	config, buildDiags := terraform.BuildConfigWithGraph(rootMod, loader.ModuleWalker(), inputValues, configs.MockDataLoaderFunc(loader.LoadExternalMockData))
	diags = diags.Append(buildDiags)
	if config == nil {
		return nil, diags
	}
	providerFactories := map[addrs.Provider]providers.Factory{}
	if s != nil && s.providerCacheOverride != nil {
		providerFactories = s.providerCacheOverride
	} else if runtime != nil {
		var err error
		providerFactories, err = providerFactoriesForLocks(runtime.Locks, runtime.ProviderCache)
		if err != nil {
			diags = diags.Append(tfdiags.Sourceless(tfdiags.Error, "Failed to initialize runbook providers", err.Error()))
		}
	}
	tfCtx, ctxDiags := terraform.NewContext(&terraform.ContextOpts{Parallelism: 1, Providers: providerFactories})
	diags = diags.Append(ctxDiags)
	if ctxDiags.HasErrors() {
		return nil, diags
	}
	plan, planDiags := tfCtx.Plan(config, states.NewState(), &terraform.PlanOpts{
		Mode:         plans.NormalMode,
		Query:        true,
		SetVariables: inputValues,
	})
	if plan != nil && plan.Changes != nil && len(plan.Changes.Queries) == 0 {
		diags = diags.Append(tfdiags.Sourceless(
			tfdiags.Warning,
			"Lowered step produced no query changes",
			"Terraform planned the lowered step but did not record any query changes for the lowered .tfquery configuration.",
		))
	}
	diags = diags.Append(planDiags)
	return plan, diags
}

func (s *runbooksServer) schemasForRunbookPlan(dir string, runtime *runbookRuntime) (*terraform.Schemas, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	loader, err := configload.NewLoader(&configload.Config{
		ModulesDir:        dir + "/.terraform/modules",
		IncludeQueryFiles: true,
	})
	if err != nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Failed to initialize lowered step loader", err.Error()))
	}
	rootMod, hclDiags := loader.LoadRootModule(dir)
	diags = diags.Append(hclDiags)
	if rootMod == nil || hclDiags.HasErrors() {
		return nil, diags
	}
	inputValues := make(terraform.InputValues)
	for name := range rootMod.Variables {
		inputValues[name] = &terraform.InputValue{Value: cty.NilVal, SourceType: terraform.ValueFromCaller}
	}
	config, buildDiags := terraform.BuildConfigWithGraph(rootMod, loader.ModuleWalker(), inputValues, configs.MockDataLoaderFunc(loader.LoadExternalMockData))
	diags = diags.Append(buildDiags)
	if config == nil {
		return nil, diags
	}
	providerFactories := map[addrs.Provider]providers.Factory{}
	if s != nil && s.providerCacheOverride != nil {
		providerFactories = s.providerCacheOverride
	} else if runtime != nil {
		var err error
		providerFactories, err = providerFactoriesForLocks(runtime.Locks, runtime.ProviderCache)
		if err != nil {
			return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Failed to initialize runbook providers", err.Error()))
		}
	}
	tfCtx, ctxDiags := terraform.NewContext(&terraform.ContextOpts{Parallelism: 1, Providers: providerFactories})
	diags = diags.Append(ctxDiags)
	if ctxDiags.HasErrors() {
		return nil, diags
	}
	ret, schemaDiags := tfCtx.Schemas(config, states.NewState())
	diags = diags.Append(schemaDiags)
	return ret, diags
}

func schemaForPlannedQuery(schemas *terraform.Schemas, q *plans.QueryInstanceSrc) *providers.Schema {
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

func (s *runbooksServer) lookupRunbookRuntime(runtimeHandle int64) (*runbookRuntime, error) {
	if runtimeHandle == 0 {
		return nil, nil
	}
	hnd := handle[*runbookRuntime](runtimeHandle)
	runtime := s.handles.RunbookRuntime(hnd)
	if runtime == nil {
		return nil, status.Error(codes.InvalidArgument, "the given runbook runtime handle is invalid")
	}
	return runtime, nil
}
