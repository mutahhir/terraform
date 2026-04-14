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

type NodeStepAction struct {
	StepName string
	Action   *configs.Action
}

func (n *NodeStepAction) Hashcode() interface{} {
	return [4]string{"step_action", n.StepName, n.Action.Type, n.Action.Name}
}

func (n *NodeStepAction) Name() string {
	return fmt.Sprintf("step.%s.action.%s.%s", n.StepName, n.Action.Type, n.Action.Name)
}

func (n *NodeStepAction) Execute(ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	if op != walkOperationPlan {
		return nil
	}
	ctx.EmitStepPlanInfo(StepPlanInfo{StepName: n.StepName, StepIndex: 0, Type: "action", Subject: n.Action.Addr().String(), Status: runbookruntime.StepStatusPlanned})
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
			value, _, valueDiags := ctx.EvaluateBlock(n.Action.Config, actionSchema.ConfigSchema)
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
		ctx.MarkActionPlanned(n.StepName, n.Action.Addr())
	}
	return diags
}

type NodeStepData struct {
	StepName string
	Data     *configs.Resource
}

func (n *NodeStepData) Hashcode() interface{} {
	return [4]string{"step_data", n.StepName, n.Data.Type, n.Data.Name}
}

func (n *NodeStepData) Name() string {
	return fmt.Sprintf("step.%s.data.%s.%s", n.StepName, n.Data.Type, n.Data.Name)
}

func (n *NodeStepData) Execute(ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	if op != walkOperationPlan {
		return nil
	}
	ctx.EmitStepPlanInfo(StepPlanInfo{StepName: n.StepName, StepIndex: 0, Type: "data", Subject: n.Data.Addr().String(), Status: runbookruntime.StepStatusPlanned})
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
		value, _, valueDiags := ctx.EvaluateBlock(n.Data.Config, resourceSchema.Body)
		diags = diags.Append(valueDiags)
		if diags.HasErrors() {
			return diags
		}
		configVal = value
	}
	resp := provider.ReadDataSource(providers.ReadDataSourceRequest{TypeName: n.Data.Type, Config: configVal, ProviderMeta: providerMetaVal})
	diags = diags.Append(resp.Diagnostics)
	if !diags.HasErrors() {
		ctx.SetStepData(n.StepName, n.Data.Addr(), resp.State)
	}
	return diags
}

type NodeStepList struct {
	StepName string
	List     *configs.Resource
}

func (n *NodeStepList) Hashcode() interface{} {
	return [4]string{"step_list", n.StepName, n.List.Type, n.List.Name}
}

func (n *NodeStepList) Name() string {
	return fmt.Sprintf("step.%s.list.%s.%s", n.StepName, n.List.Type, n.List.Name)
}

func (n *NodeStepList) Execute(ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	if op != walkOperationPlan {
		return nil
	}
	ctx.EmitStepPlanInfo(StepPlanInfo{StepName: n.StepName, StepIndex: 0, Type: "list", Subject: n.List.Addr().String(), Status: runbookruntime.StepStatusPlanned})
	providerType := providerTypeForResource(ctx.Config(), n.List)
	provider, diags := runbookProvider(ctx, providerType)
	if diags.HasErrors() {
		return diags
	}
	schemaResp := provider.GetProviderSchema()
	listSchema := schemaResp.SchemaForListResourceType(n.List.Type)
	blockVal := cty.EmptyObjectVal
	if listSchema.FullSchema != nil {
		value, _, valueDiags := ctx.EvaluateBlock(n.List.Config, listSchema.FullSchema)
		diags = diags.Append(valueDiags)
		if diags.HasErrors() {
			return diags
		}
		blockVal = value
	}
	includeResource := false
	if n.List.List != nil && n.List.List.IncludeResource != nil {
		value, valueDiags := ctx.EvaluateExpr(n.StepName, n.List.List.IncludeResource)
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
		value, valueDiags := ctx.EvaluateExpr(n.StepName, n.List.List.Limit)
		diags = diags.Append(valueDiags)
		if diags.HasErrors() {
			return diags
		}
		if value.IsKnown() && !value.IsNull() {
			bf := value.AsBigFloat()
			limit, _ = bf.Int64()
		}
	}
	unmarkedBlockVal, _ := blockVal.UnmarkDeep()
	if !unmarkedBlockVal.IsNull() && listSchema.ConfigSchema != nil && unmarkedBlockVal.Type().HasAttribute("config") && unmarkedBlockVal.GetAttr("config").IsNull() {
		mp := unmarkedBlockVal.AsValueMap()
		mp["config"] = listSchema.ConfigSchema.EmptyValue()
		unmarkedBlockVal = cty.ObjectVal(mp)
	}
	resp := provider.ListResource(providers.ListResourceRequest{TypeName: n.List.Type, Config: unmarkedBlockVal, IncludeResourceObject: includeResource, Limit: limit})
	diags = diags.Append(resp.Diagnostics)
	if !diags.HasErrors() {
		ctx.SetStepList(n.StepName, n.List.Addr(), resp.Result)
	}
	return diags
}

type NodeStepLocal struct {
	StepName string
	Local    *configs.Local
}

func (n *NodeStepLocal) Hashcode() interface{} {
	return [3]string{"step_local", n.StepName, n.Local.Name}
}

func (n *NodeStepLocal) Name() string {
	return fmt.Sprintf("step.%s.local.%s", n.StepName, n.Local.Name)
}

func (n *NodeStepLocal) Execute(ctx *EvalContext, _ walkOperation) tfdiags.Diagnostics {
	ctx.EmitStepPlanInfo(StepPlanInfo{StepName: n.StepName, StepIndex: 0, Type: "local", Subject: n.Local.Name, Status: runbookruntime.StepStatusPlanned})
	value, diags := ctx.EvaluateExpr(n.StepName, n.Local.Expr)
	if diags.HasErrors() {
		return diags
	}
	ctx.SetStepLocal(n.StepName, n.Local.Name, value)
	return nil
}

type NodeStepExecution struct {
	StepName  string
	Index     int
	Execution *runbookconfigs.Execution
}

func (n *NodeStepExecution) Hashcode() interface{} {
	return [3]interface{}{"step_execution", n.StepName, n.Index}
}

func (n *NodeStepExecution) Name() string {
	return fmt.Sprintf("step.%s.execute.%d", n.StepName, n.Index)
}

func (n *NodeStepExecution) Execute(ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	if op != walkOperationExecute {
		return nil
	}
	ctx.EmitStepPlanInfo(StepPlanInfo{StepName: n.StepName, StepIndex: 0, Type: "execute", Subject: fmt.Sprintf("execute.%d", n.Index), Status: runbookruntime.StepStatusRunning})
	var diags tfdiags.Diagnostics
	providerCache := map[terraformaddrs.Provider]providers.Interface{}
	for _, traversal := range n.Execution.InvokeAction {
		ref, refDiags := runbookaddrs.ParseRef(traversal)
		diags = diags.Append(refDiags)
		if refDiags.HasErrors() || ref == nil {
			continue
		}
		actionAddr, ok := ref.Subject.(terraformaddrs.Action)
		if !ok {
			continue
		}
		action := actionConfigForStep(ctx.Config(), n.StepName, actionAddr)
		if action == nil {
			action = workspaceActionConfig(ctx.WorkspaceConfig(), actionAddr)
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
		resp := provider.InvokeAction(providers.InvokeActionRequest{ActionType: action.Type, PlannedActionData: cty.EmptyObjectVal})
		diags = diags.Append(resp.Diagnostics)
		if resp.Events != nil {
			for event := range resp.Events {
				if completed, ok := event.(providers.InvokeActionEvent_Completed); ok {
					diags = diags.Append(completed.Diagnostics)
				}
			}
		}
		if !diags.HasErrors() {
			ctx.MarkActionInvoked(n.StepName, actionAddr)
		}
	}
	return diags
}

type NodeStepCondition struct {
	StepName  string
	Condition *runbookconfigs.Condition
}

func (n *NodeStepCondition) Hashcode() interface{} {
	return [3]interface{}{"step_condition", n.StepName, n.Condition.DeclRange.String()}
}

func (n *NodeStepCondition) Name() string {
	return fmt.Sprintf("step.%s.%s.%s", n.StepName, n.Condition.Kind, n.Condition.DeclRange.String())
}

func (n *NodeStepCondition) Execute(ctx *EvalContext, _ walkOperation) tfdiags.Diagnostics {
	ctx.EmitStepPlanInfo(StepPlanInfo{StepName: n.StepName, StepIndex: 0, Type: string(n.Condition.Kind), Subject: n.Condition.DeclRange.String(), Status: runbookruntime.StepStatusPlanned})
	value, diags := ctx.EvaluateExpr(n.StepName, n.Condition.Condition)
	if diags.HasErrors() {
		ctx.SetStepStatus(n.StepName, runbookruntime.StepStatusFailed, "condition evaluation failed")
		return diags
	}
	if !value.IsKnown() || value.IsNull() || value.False() {
		message := fmt.Sprintf("%s failed", n.Condition.Kind)
		if n.Condition.ErrorMessage != nil {
			msgVal, msgDiags := ctx.EvaluateExpr(n.StepName, n.Condition.ErrorMessage)
			diags = diags.Append(msgDiags)
			if !msgDiags.HasErrors() && msgVal.IsKnown() && !msgVal.IsNull() {
				message = msgVal.AsString()
			}
		}
		if n.Condition.OnFail == runbookconfigs.ConditionOnFailSkip {
			ctx.SetStepStatus(n.StepName, runbookruntime.StepStatusSkipped, message)
			return diags
		}
		ctx.SetStepStatus(n.StepName, runbookruntime.StepStatusFailed, message)
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
	StepName    string
	InstanceKey terraformaddrs.InstanceKey
	Output      *configs.Output
}

func (n *NodeStepOutput) Hashcode() interface{} {
	key := ""
	if n.InstanceKey != nil {
		key = n.InstanceKey.String()
	}
	return [4]string{"step_output", n.StepName, key, n.Output.Name}
}

func (n *NodeStepOutput) Name() string {
	if n.InstanceKey == nil {
		return fmt.Sprintf("step.%s.%s", n.StepName, n.Output.Name)
	}
	return fmt.Sprintf("step.%s%s.%s", n.StepName, n.InstanceKey.String(), n.Output.Name)
}

func (n *NodeStepOutput) Execute(ctx *EvalContext, _ walkOperation) tfdiags.Diagnostics {
	ctx.EmitStepPlanInfo(StepPlanInfo{StepName: n.StepName, StepIndex: 0, Type: "output", Subject: n.Output.Name, Status: runbookruntime.StepStatusPlanned})
	value, diags := ctx.EvaluateExpr(n.StepName, n.Output.Expr)
	if diags.HasErrors() {
		return diags
	}
	ctx.SetStepOutput(n.StepName, n.Output.Name, value)
	if step, ok := ctx.Step(n.StepName); ok && step.Status == runbookruntime.StepStatusPlanned {
		ctx.SetStepStatus(n.StepName, runbookruntime.StepStatusCompleted, "")
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

func workspaceActionConfig(config *configs.Config, addr terraformaddrs.Action) *configs.Action {
	if config == nil || config.Module == nil {
		return nil
	}
	return config.Module.Actions[addr.String()]
}
