package runbookgraph

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/providers"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

func requireStepInstance(step *NodeStepInstance) tfdiags.Diagnostics {
	if step != nil {
		return nil
	}
	return tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Missing step instance",
		Detail:   "Runbook step inner nodes must always have an owning step instance.",
	})
}

func stepRuntimeIndex(step *NodeStepInstance) int {
	if step == nil || step.Runtime == nil {
		return 0
	}
	return step.Runtime.Index
}

type NodeStepAction struct {
	Step   *NodeStepInstance
	Action *configs.Action
}

func (n *NodeStepAction) Hashcode() interface{} {
	key := ""
	if n.Step != nil && n.Step.InstanceKey != nil {
		key = n.Step.InstanceKey.String()
	}
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
	}
	return [5]string{"step_action", stepName, key, n.Action.Type, n.Action.Name}
}

func (n *NodeStepAction) Name() string {
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
	}
	if n.Step == nil || n.Step.InstanceKey == nil {
		return fmt.Sprintf("step.%s.action.%s.%s", stepName, n.Action.Type, n.Action.Name)
	}
	return fmt.Sprintf("step.%s%s.action.%s.%s", stepName, n.Step.InstanceKey.String(), n.Action.Type, n.Action.Name)
}

func (n *NodeStepAction) Execute(ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	if diags := requireStepInstance(n.Step); diags.HasErrors() {
		return diags
	}
	if op != walkOperationPlan {
		return nil
	}
	ctx.EmitStepPlanInfo(StepPlanInfo{StepName: n.Step.StepName, StepIndex: stepRuntimeIndex(n.Step), Type: "action", Subject: n.Action.Addr().String(), Status: runbookruntime.StepStatusPlanned})
	providerType := providerTypeForAction(ctx.Config(), n.Action)
	provider, diags := runbookProvider(ctx, providerType)
	if diags.HasErrors() {
		return diags
	}
	configVal := cty.EmptyObjectVal
	if n.Action.Config != nil {
		schemaResp := provider.GetProviderSchema()
		actionSchema := schemaResp.Actions[n.Action.Type]
		if actionSchema.ConfigSchema != nil {
			if localVal, ok := ctx.stepLocalWithKey(n.Step.StepName, n.Step.InstanceKey, "selected_id"); ok {
				_ = localVal
			}
			value, _, valueDiags := ctx.EvaluateBlockForInstance(n.Step.StepName, n.Step.InstanceKey, n.Step.RepetitionData, n.Action.Config, actionSchema.ConfigSchema)
			diags = diags.Append(valueDiags)
			if diags.HasErrors() {
				return diags
			}
			configVal = value
		}
	}
	resp := provider.PlanAction(providers.PlanActionRequest{ActionType: n.Action.Type, ProposedActionData: configVal})
	diags = diags.Append(resp.Diagnostics)
	if !diags.HasErrors() {
		ctx.setActionPlannedWithKey(n.Step.StepName, n.Step.InstanceKey, n.Action.Addr(), configVal)
	}
	return diags
}

type NodeStepData struct {
	Step *NodeStepInstance
	Data *configs.Resource
}

func (n *NodeStepData) Hashcode() interface{} {
	key := ""
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
		if n.Step.InstanceKey != nil {
			key = n.Step.InstanceKey.String()
		}
	}
	return [5]string{"step_data", stepName, key, n.Data.Type, n.Data.Name}
}

func (n *NodeStepData) Name() string {
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
	}
	if n.Step == nil || n.Step.InstanceKey == nil {
		return fmt.Sprintf("step.%s.data.%s.%s", stepName, n.Data.Type, n.Data.Name)
	}
	return fmt.Sprintf("step.%s%s.data.%s.%s", stepName, n.Step.InstanceKey.String(), n.Data.Type, n.Data.Name)
}

func (n *NodeStepData) Execute(ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	if diags := requireStepInstance(n.Step); diags.HasErrors() {
		return diags
	}
	if op != walkOperationPlan {
		return nil
	}
	providerType := providerTypeForResource(ctx.Config(), n.Data)
	provider, diags := runbookProvider(ctx, providerType)
	if diags.HasErrors() {
		return diags
	}
	schemaResp := provider.GetProviderSchema()
	resourceSchema := schemaResp.SchemaForResourceAddr(n.Data.Addr())
	configVal := cty.EmptyObjectVal
	providerMetaVal := cty.EmptyObjectVal
	if schemaResp.ProviderMeta.Body != nil {
		providerMetaVal = schemaResp.ProviderMeta.Body.EmptyValue()
	}
	if resourceSchema.Body != nil {
		value, _, valueDiags := ctx.EvaluateBlockForInstance(n.Step.StepName, n.Step.InstanceKey, n.Step.RepetitionData, n.Data.Config, resourceSchema.Body)
		diags = diags.Append(valueDiags)
		if diags.HasErrors() {
			return diags
		}
		configVal = value
	}
	ctx.EmitStepPlanInfo(StepPlanInfo{
		StepName:  n.Step.StepName,
		StepIndex: stepRuntimeIndex(n.Step),
		Type:      "data",
		Subject:   n.Data.Addr().String(),
		Status:    runbookruntime.StepStatusPlanned,
		Details: cty.ObjectVal(map[string]cty.Value{
			"provider": cty.StringVal(providerType.ForDisplay()),
			"config":   configVal,
		}),
	})
	resp := provider.ReadDataSource(providers.ReadDataSourceRequest{TypeName: n.Data.Type, Config: configVal, ProviderMeta: providerMetaVal})
	diags = diags.Append(resp.Diagnostics)
	if !diags.HasErrors() {
		ctx.setStepValueWithKey(n.Step.StepName, n.Step.InstanceKey, func(state *stepEvalState) {
			state.data[n.Data.Addr().String()] = resp.State
		})
	}
	return diags
}

type NodeStepList struct {
	Step *NodeStepInstance
	List *configs.Resource
}

func (n *NodeStepList) Hashcode() interface{} {
	key := ""
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
		if n.Step.InstanceKey != nil {
			key = n.Step.InstanceKey.String()
		}
	}
	return [5]string{"step_list", stepName, key, n.List.Type, n.List.Name}
}

func (n *NodeStepList) Name() string {
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
	}
	if n.Step == nil || n.Step.InstanceKey == nil {
		return fmt.Sprintf("step.%s.list.%s.%s", stepName, n.List.Type, n.List.Name)
	}
	return fmt.Sprintf("step.%s%s.list.%s.%s", stepName, n.Step.InstanceKey.String(), n.List.Type, n.List.Name)
}

func (n *NodeStepList) Execute(ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	if diags := requireStepInstance(n.Step); diags.HasErrors() {
		return diags
	}
	if op != walkOperationPlan {
		return nil
	}
	providerType := providerTypeForResource(ctx.Config(), n.List)
	provider, diags := runbookProvider(ctx, providerType)
	if diags.HasErrors() {
		return diags
	}
	schemaResp := provider.GetProviderSchema()
	listSchema := schemaResp.SchemaForListResourceType(n.List.Type)
	blockVal := cty.EmptyObjectVal
	if listSchema.FullSchema != nil {
		value, _, valueDiags := ctx.EvaluateBlockForInstance(n.Step.StepName, n.Step.InstanceKey, n.Step.RepetitionData, n.List.Config, listSchema.FullSchema)
		diags = diags.Append(valueDiags)
		if diags.HasErrors() {
			return diags
		}
		blockVal = value
	}
	includeResource := false
	if n.List.List != nil && n.List.List.IncludeResource != nil {
		value, valueDiags := ctx.EvaluateExprForInstance(n.Step.StepName, n.Step.InstanceKey, n.Step.RepetitionData, n.List.List.IncludeResource)
		diags = diags.Append(valueDiags)
		if diags.HasErrors() {
			return diags
		}
		if value.IsKnown() && !value.IsNull() {
			includeResource = value.True()
		}
	}
	var limit int64
	if n.List.List != nil && n.List.List.Limit != nil {
		value, valueDiags := ctx.EvaluateExprForInstance(n.Step.StepName, n.Step.InstanceKey, n.Step.RepetitionData, n.List.List.Limit)
		diags = diags.Append(valueDiags)
		if diags.HasErrors() {
			return diags
		}
		if value.IsKnown() && !value.IsNull() {
			bf := value.AsBigFloat()
			limit, _ = bf.Int64()
		}
	}
	ctx.EmitStepPlanInfo(StepPlanInfo{
		StepName:  n.Step.StepName,
		StepIndex: stepRuntimeIndex(n.Step),
		Type:      "list",
		Subject:   n.List.Addr().String(),
		Status:    runbookruntime.StepStatusPlanned,
		Details: cty.ObjectVal(map[string]cty.Value{
			"provider":         cty.StringVal(providerType.ForDisplay()),
			"config":           blockVal,
			"include_resource": cty.BoolVal(includeResource),
			"limit":            cty.NumberIntVal(limit),
		}),
	})
	unmarkedBlockVal, _ := blockVal.UnmarkDeep()
	if !unmarkedBlockVal.IsNull() && listSchema.ConfigSchema != nil && unmarkedBlockVal.Type().HasAttribute("config") && unmarkedBlockVal.GetAttr("config").IsNull() {
		mp := unmarkedBlockVal.AsValueMap()
		mp["config"] = listSchema.ConfigSchema.EmptyValue()
		unmarkedBlockVal = cty.ObjectVal(mp)
	}
	resp := provider.ListResource(providers.ListResourceRequest{TypeName: n.List.Type, Config: unmarkedBlockVal, IncludeResourceObject: includeResource, Limit: limit})
	diags = diags.Append(resp.Diagnostics)
	if !diags.HasErrors() {
		ctx.setStepValueWithKey(n.Step.StepName, n.Step.InstanceKey, func(state *stepEvalState) {
			state.lists[n.List.Addr().String()] = resp.Result
		})
	}
	return diags
}

type NodeStepLocal struct {
	Step  *NodeStepInstance
	Local *configs.Local
}

func (n *NodeStepLocal) Hashcode() interface{} {
	key := ""
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
		if n.Step.InstanceKey != nil {
			key = n.Step.InstanceKey.String()
		}
	}
	return [4]string{"step_local", stepName, key, n.Local.Name}
}

func (n *NodeStepLocal) Name() string {
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
	}
	if n.Step == nil || n.Step.InstanceKey == nil {
		return fmt.Sprintf("step.%s.local.%s", stepName, n.Local.Name)
	}
	return fmt.Sprintf("step.%s%s.local.%s", stepName, n.Step.InstanceKey.String(), n.Local.Name)
}

func (n *NodeStepLocal) Execute(ctx *EvalContext, _ walkOperation) tfdiags.Diagnostics {
	if diags := requireStepInstance(n.Step); diags.HasErrors() {
		return diags
	}
	ctx.EmitStepPlanInfo(StepPlanInfo{StepName: n.Step.StepName, StepIndex: stepRuntimeIndex(n.Step), Type: "local", Subject: n.Local.Name, Status: runbookruntime.StepStatusPlanned})
	value, diags := ctx.EvaluateExprForInstance(n.Step.StepName, n.Step.InstanceKey, n.Step.RepetitionData, n.Local.Expr)
	if diags.HasErrors() {
		return diags
	}
	ctx.setStepValueWithKey(n.Step.StepName, n.Step.InstanceKey, func(state *stepEvalState) {
		state.locals[n.Local.Name] = value
	})
	return nil
}

type NodeStepExecution struct {
	Step      *NodeStepInstance
	Index     int
	Execution *runbookconfigs.Execution
}

func (n *NodeStepExecution) Hashcode() interface{} {
	key := ""
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
		if n.Step.InstanceKey != nil {
			key = n.Step.InstanceKey.String()
		}
	}
	return [4]interface{}{"step_execution", stepName, key, n.Index}
}

func (n *NodeStepExecution) Name() string {
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
	}
	if n.Step == nil || n.Step.InstanceKey == nil {
		return fmt.Sprintf("step.%s.execute.%d", stepName, n.Index)
	}
	return fmt.Sprintf("step.%s%s.execute.%d", stepName, n.Step.InstanceKey.String(), n.Index)
}

func (n *NodeStepExecution) Execute(ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	if diags := requireStepInstance(n.Step); diags.HasErrors() {
		return diags
	}
	for _, traversal := range n.Execution.InvokeAction {
		subject := fmt.Sprintf("execute.%d", n.Index)
		value := cty.NilVal
		if ref, refDiags := runbookaddrs.ParseRef(traversal); !refDiags.HasErrors() && ref != nil {
			subject = ref.Subject.String()
			if actionAddr, ok := ref.Subject.(terraformaddrs.Action); ok {
				if plannedConfig, ok := ctx.actionPlannedConfigWithKey(n.Step.StepName, n.Step.InstanceKey, actionAddr); ok {
					value = plannedConfig
				}
			}
		}
		ctx.EmitStepPlanInfo(StepPlanInfo{StepName: n.Step.StepName, StepIndex: stepRuntimeIndex(n.Step), Type: "execute", Subject: subject, Status: runbookruntime.StepStatusPlanned, Value: value})
	}
	if op != walkOperationExecute {
		return nil
	}
	var diags tfdiags.Diagnostics
	providerCache := map[terraformaddrs.Provider]providers.Interface{}
	for _, traversal := range n.Execution.InvokeAction {
		ref, refDiags := runbookaddrs.ParseRef(traversal)
		diags = diags.Append(refDiags)
		if refDiags.HasErrors() || ref == nil {
			continue
		}
		var actionAddr terraformaddrs.Action
		var action *configs.Action
		switch subject := ref.Subject.(type) {
		case terraformaddrs.Action:
			actionAddr = subject
			action = actionConfigForStep(ctx.Config(), n.Step.StepName, actionAddr)
		case runbookaddrs.WorkspaceAction:
			actionAddr = subject.Action
			action = workspaceActionConfig(ctx.WorkspaceConfig(), subject)
		default:
			continue
		}
		if action == nil {
			continue
		}
		providerType := providerTypeForAction(ctx.Config(), action)
		provider, ok := providerCache[providerType]
		if !ok {
			var providerDiags tfdiags.Diagnostics
			provider, providerDiags = runbookProvider(ctx, providerType)
			diags = diags.Append(providerDiags)
			if providerDiags.HasErrors() {
				continue
			}
			providerCache[providerType] = provider
		}
		plannedConfig := cty.EmptyObjectVal
		if existing, ok := ctx.actionPlannedConfigWithKey(n.Step.StepName, n.Step.InstanceKey, actionAddr); ok {
			plannedConfig = existing
		}
		resp := provider.InvokeAction(providers.InvokeActionRequest{ActionType: action.Type, PlannedActionData: plannedConfig})
		diags = diags.Append(resp.Diagnostics)
		if resp.Events != nil {
			for event := range resp.Events {
				if completed, ok := event.(providers.InvokeActionEvent_Completed); ok {
					diags = diags.Append(completed.Diagnostics)
				}
			}
		}
		if !diags.HasErrors() {
			ctx.setStepValueWithKey(n.Step.StepName, n.Step.InstanceKey, func(state *stepEvalState) {
				actionState, ok := state.actions[actionAddr.String()]
				if !ok {
					actionState = &actionEvalState{}
					state.actions[actionAddr.String()] = actionState
				}
				actionState.invoked = true
			})
		}
	}
	return diags
}

type NodeStepCondition struct {
	Step      *NodeStepInstance
	Condition *runbookconfigs.Condition
}

func (n *NodeStepCondition) Hashcode() interface{} {
	key := ""
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
		if n.Step.InstanceKey != nil {
			key = n.Step.InstanceKey.String()
		}
	}
	return [4]interface{}{"step_condition", stepName, key, n.Condition.DeclRange.String()}
}

func (n *NodeStepCondition) Name() string {
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
	}
	if n.Step == nil || n.Step.InstanceKey == nil {
		return fmt.Sprintf("step.%s.%s.%s", stepName, n.Condition.Kind, n.Condition.DeclRange.String())
	}
	return fmt.Sprintf("step.%s%s.%s.%s", stepName, n.Step.InstanceKey.String(), n.Condition.Kind, n.Condition.DeclRange.String())
}

func (n *NodeStepCondition) Execute(ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	if diags := requireStepInstance(n.Step); diags.HasErrors() {
		return diags
	}
	ctx.EmitStepPlanInfo(StepPlanInfo{StepName: n.Step.StepName, StepIndex: stepRuntimeIndex(n.Step), Type: string(n.Condition.Kind), Subject: n.Condition.DeclRange.String(), Status: runbookruntime.StepStatusPlanned})
	value, diags := ctx.EvaluateExprForInstance(n.Step.StepName, n.Step.InstanceKey, n.Step.RepetitionData, n.Condition.Condition)
	if diags.HasErrors() {
		ctx.setStepStatusWithKey(n.Step.StepName, n.Step.InstanceKey, runbookruntime.StepStatusFailed, "condition evaluation failed")
		return diags
	}
	if op == walkOperationPlan && n.Condition.Kind == runbookconfigs.PostconditionCondition {
		return diags
	}
	if !value.IsKnown() || value.IsNull() || value.False() {
		message := fmt.Sprintf("%s failed", n.Condition.Kind)
		if n.Condition.ErrorMessage != nil {
			msgVal, msgDiags := ctx.EvaluateExprForInstance(n.Step.StepName, n.Step.InstanceKey, n.Step.RepetitionData, n.Condition.ErrorMessage)
			diags = diags.Append(msgDiags)
			if !msgDiags.HasErrors() && msgVal.IsKnown() && !msgVal.IsNull() {
				message = msgVal.AsString()
			}
		}
		if n.Condition.OnFail == runbookconfigs.ConditionOnFailSkip {
			ctx.setStepStatusWithKey(n.Step.StepName, n.Step.InstanceKey, runbookruntime.StepStatusSkipped, message)
			return diags
		}
		ctx.setStepStatusWithKey(n.Step.StepName, n.Step.InstanceKey, runbookruntime.StepStatusFailed, message)
		return diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("%s failed", n.Condition.Kind),
			Detail:   message,
			Subject:  n.Condition.DeclRange.Ptr(),
		})
	}
	return diags
}

type NodeStepOutput struct {
	Step   *NodeStepInstance
	Output *configs.Output
}

func (n *NodeStepOutput) Hashcode() interface{} {
	key := ""
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
		if n.Step.InstanceKey != nil {
			key = n.Step.InstanceKey.String()
		}
	}
	return [4]string{"step_output", stepName, key, n.Output.Name}
}

func (n *NodeStepOutput) Name() string {
	stepName := ""
	if n.Step != nil {
		stepName = n.Step.StepName
	}
	if n.Step == nil || n.Step.InstanceKey == nil {
		return fmt.Sprintf("step.%s.%s", stepName, n.Output.Name)
	}
	return fmt.Sprintf("step.%s%s.%s", stepName, n.Step.InstanceKey.String(), n.Output.Name)
}

func (n *NodeStepOutput) Execute(ctx *EvalContext, _ walkOperation) tfdiags.Diagnostics {
	if diags := requireStepInstance(n.Step); diags.HasErrors() {
		return diags
	}
	ctx.EmitStepPlanInfo(StepPlanInfo{StepName: n.Step.StepName, StepIndex: stepRuntimeIndex(n.Step), Type: "output", Subject: n.Output.Name, Status: runbookruntime.StepStatusPlanned})
	value, diags := ctx.EvaluateExprForInstance(n.Step.StepName, n.Step.InstanceKey, n.Step.RepetitionData, n.Output.Expr)
	if diags.HasErrors() {
		return diags
	}
	ctx.setStepOutputWithKey(n.Step.StepName, n.Step.InstanceKey, n.Output.Name, value)
	if ctx.stepHasStatusWithKey(n.Step.StepName, n.Step.InstanceKey, runbookruntime.StepStatusPlanned) {
		ctx.setStepStatusWithKey(n.Step.StepName, n.Step.InstanceKey, runbookruntime.StepStatusCompleted, "")
	}
	return nil
}

func runbookProvider(ctx *EvalContext, providerType terraformaddrs.Provider) (providers.Interface, tfdiags.Diagnostics) {
	if provider, ok := ctx.Provider(providerType); ok {
		configVal, diags := runbookProviderConfigValue(ctx, providerType)
		if diags.HasErrors() {
			return nil, diags
		}
		resp := provider.ConfigureProvider(providers.ConfigureProviderRequest{Config: configVal})
		if resp.Diagnostics.HasErrors() {
			return nil, resp.Diagnostics
		}
		return provider, nil
	}
	return nil, missingProviderDiagnostic(providerType, nil)
}

func runbookProviderConfigValue(ctx *EvalContext, providerType terraformaddrs.Provider) (cty.Value, tfdiags.Diagnostics) {
	config := ctx.Config()
	providerConfig := providerConfigForType(config, providerType)
	if providerConfig == nil {
		return cty.EmptyObjectVal, nil
	}

	addr := terraformaddrs.AbsProviderConfig{
		Module:   terraformaddrs.RootModule,
		Provider: providerType,
		Alias:    providerConfig.Alias,
	}
	configBody := buildRunbookProviderConfig(ctx, addr, providerConfig)

	schemaResp, diags := providerSchemaForExecution(ctx, providerType)
	if diags.HasErrors() {
		return cty.NilVal, diags
	}
	configSchema := schemaResp.Provider.Body
	if configSchema == nil {
		return cty.EmptyObjectVal, nil
	}

	configVal, _, evalDiags := ctx.EvaluateBlock(configBody, configSchema)
	diags = diags.Append(evalDiags)
	if diags.HasErrors() {
		return cty.NilVal, diags
	}
	unmarkedConfigVal, _ := configVal.UnmarkDeep()
	return unmarkedConfigVal, nil
}

func providerSchemaForExecution(ctx *EvalContext, providerType terraformaddrs.Provider) (providers.ProviderSchema, tfdiags.Diagnostics) {
	provider, ok := ctx.Provider(providerType)
	if !ok {
		return providers.ProviderSchema{}, missingProviderDiagnostic(providerType, nil)
	}
	resp := provider.GetProviderSchema()
	if resp.Diagnostics.HasErrors() {
		return providers.ProviderSchema{}, resp.Diagnostics
	}
	return resp, nil
}

func providerConfigForType(config *runbookconfigs.RunbookConfig, providerType terraformaddrs.Provider) *configs.Provider {
	if config == nil {
		return nil
	}
	for _, providerConfig := range config.ProviderConfigs {
		if providerConfig != nil && providerTypeForConfig(config, providerConfig) == providerType && providerConfig.Alias == "" {
			return providerConfig
		}
	}
	return nil
}

func missingProviderDiagnostic(providerType terraformaddrs.Provider, subject *hcl.Range) tfdiags.Diagnostics {
	return tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Missing provider",
		Detail:   fmt.Sprintf("Provider %s is not configured for runbook execution.", providerType.ForDisplay()),
		Subject:  subject,
	})
}

func actionConfigForStep(config *runbookconfigs.RunbookConfig, stepName string, addr terraformaddrs.Action) *configs.Action {
	if config == nil {
		return nil
	}
	step, ok := config.Steps[stepName]
	if !ok || step == nil {
		return nil
	}
	for _, action := range step.Actions {
		if action != nil && action.Type == addr.Type && action.Name == addr.Name {
			return action
		}
	}
	return nil
}

func workspaceActionConfig(config *configs.Config, addr runbookaddrs.WorkspaceAction) *configs.Action {
	if config == nil || config.Module == nil {
		return nil
	}
	target := config
	for _, call := range addr.Module.Calls {
		child, ok := target.Children[call.Name]
		if !ok || child == nil {
			return nil
		}
		target = child
	}
	if target.Module == nil {
		return nil
	}
	return target.Module.Actions[addr.Action.String()]
}
