// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package rpcapi

import (
	"context"

	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configload"
	"github.com/hashicorp/terraform/internal/plans"
	"github.com/zclconf/go-cty/cty"
	ctymsgpack "github.com/zclconf/go-cty/cty/msgpack"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/runbooks"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/states"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type runbooksServer struct {
	runbooks.UnimplementedRunbooksServer

	handles *handleTable
}

func newRunbooksServer(handles *handleTable) *runbooksServer {
	return &runbooksServer{handles: handles}
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
	if plan.Lowered != nil {
		lowerDiags := validateAndPlanLoweredStepDir(plan.Lowered.Dir)
		plan.Evaluation.Diags = plan.Evaluation.Diags.Append(lowerDiags)
	}
	plannedActions := make([]*runbooks.PlanRunbookStep_PlannedAction, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		plannedActions = append(plannedActions, &runbooks.PlanRunbookStep_PlannedAction{
			Address:    action.Address,
			ActionType: action.ActionType,
			ActionName: action.ActionName,
		})
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
		for _, provider := range cfg.Runbook.Providers {
			ret.Providers = append(ret.Providers, &runbooks.FindRunbookConfigurationSteps_ProviderConfig{
				Type: provider.Type,
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

	return runbookconfig.EvalScope{
		Variables: variables,
		List:      list,
		Actions:   actions,
		Steps:     steps,
		Locals:    locals,
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

func validateAndPlanLoweredStepDir(dir string) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	loader, err := configload.NewLoader(&configload.Config{
		ModulesDir:        dir + "/.terraform/modules",
		IncludeQueryFiles: true,
	})
	if err != nil {
		return diags.Append(tfdiags.Sourceless(tfdiags.Error, "Failed to initialize lowered step loader", err.Error()))
	}
	rootMod, hclDiags := loader.LoadRootModule(dir)
	diags = diags.Append(hclDiags)
	if rootMod == nil || hclDiags.HasErrors() {
		return diags
	}
	config, buildDiags := terraform.BuildConfigWithGraph(rootMod, loader.ModuleWalker(), terraform.InputValues{}, configs.MockDataLoaderFunc(loader.LoadExternalMockData))
	diags = diags.Append(buildDiags)
	if config == nil {
		return diags
	}
	tfCtx, ctxDiags := terraform.NewContext(&terraform.ContextOpts{Parallelism: 1})
	diags = diags.Append(ctxDiags)
	if ctxDiags.HasErrors() {
		return diags
	}
	_, planDiags := tfCtx.Plan(config, states.NewState(), &terraform.PlanOpts{
		Mode:  plans.NormalMode,
		Query: true,
	})
	diags = diags.Append(planDiags)
	return diags
}
