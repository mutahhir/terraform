package runbookgraph

import (
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/providers"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

// executeCatchBlocks runs all matching catch blocks for a failed step.
// It is called after a step fails but before error propagation.
// Catch blocks execute sequentially in declaration order.
// A catch block failure does NOT prevent subsequent catches from running.
// The failed_step object is always the original failed step (never changes).
// The failed step is threaded through the catch evaluation chain per-call
// (rather than stored on the shared context) so that concurrent step failures
// running catch blocks cannot overwrite each other's failed_step
// (hc-terraform-wdc.4).
func executeCatchBlocks(ctx EvalContext, failedStep *runbookruntime.Step, failDiags tfdiags.Diagnostics) tfdiags.Diagnostics {
	config := ctx.Config()
	if config == nil || len(config.Catches) == 0 {
		return nil
	}

	// Get catches in declaration order (sorted by name for determinism)
	catches := sortedCatches(config.Catches)
	if len(catches) == 0 {
		return nil
	}

	var catchDiags tfdiags.Diagnostics

	for _, catch := range catches {
		shouldRun, precondDiags := evaluateCatchPreconditions(ctx, catch, failedStep, failDiags)
		catchDiags = catchDiags.Append(precondDiags)

		if !shouldRun {
			emitCatchSkipped(ctx, catch.Name, "precondition not met")
			continue
		}

		emitCatchTriggered(ctx, catch.Name, failedStep.Name)
		bodyDiags := executeCatchBody(ctx, catch, failedStep, failDiags)
		if bodyDiags.HasErrors() {
			// Log catch failure as warning — do not re-enter catch loop
			emitCatchFailed(ctx, catch.Name, bodyDiags.Err().Error())
			catchDiags = catchDiags.Append(bodyDiags)
		} else {
			emitCatchCompleted(ctx, catch.Name, failedStep.Name)
		}
	}

	return catchDiags
}

// evaluateCatchPreconditions checks all preconditions for a catch block.
// Returns true if all preconditions pass (or there are none).
func evaluateCatchPreconditions(ctx EvalContext, catch *runbookconfigs.Catch, failedStep *runbookruntime.Step, failDiags tfdiags.Diagnostics) (bool, tfdiags.Diagnostics) {
	if len(catch.Preconditions) == 0 {
		return true, nil
	}

	var diags tfdiags.Diagnostics
	for _, pre := range catch.Preconditions {
		val, evalDiags := ctx.EvaluateCatchExpr(failedStep, failDiags, pre.Condition)
		diags = diags.Append(evalDiags)
		if evalDiags.HasErrors() {
			return false, diags
		}
		if !val.IsKnown() || val.IsNull() || val.False() {
			return false, diags
		}
	}
	return true, diags
}

// executeCatchBody runs the body of a catch block: data sources, locals,
// actions + execute blocks, and outputs.
func executeCatchBody(ctx EvalContext, catch *runbookconfigs.Catch, failedStep *runbookruntime.Step, failDiags tfdiags.Diagnostics) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics

	// Evaluate locals
	for _, local := range catch.Locals {
		value, evalDiags := ctx.EvaluateCatchExpr(failedStep, failDiags, local.Expr)
		diags = diags.Append(evalDiags)
		if evalDiags.HasErrors() {
			return diags
		}
		// Store local in a temporary scope — for now locals within catch
		// are evaluated but not stored (they'd need a catch-scoped eval state).
		// The value is available for subsequent expressions in the same catch
		// via the existing step-local mechanism if needed.
		_ = value
	}

	// Execute data sources via execute blocks (read_datasource operations)
	// and invoke actions via execute blocks (invoke_action operations)
	for _, execution := range catch.Executions {
		execDiags := executeCatchExecution(ctx, catch, execution, failedStep, failDiags)
		diags = diags.Append(execDiags)
		if execDiags.HasErrors() {
			return diags
		}
	}

	// Evaluate and store outputs
	for _, output := range catch.Outputs {
		value, evalDiags := ctx.EvaluateCatchExpr(failedStep, failDiags, output.Expr)
		diags = diags.Append(evalDiags)
		if evalDiags.HasErrors() {
			continue
		}
		builtinCtx, ok := ctx.(*BuiltinEvalContext)
		if ok {
			builtinCtx.SetCatchOutput(catch.Name, output.Name, value)
		}
	}

	return diags
}

// executeCatchExecution runs the operations within a single execute block
// of a catch. This handles invoke_action and read_datasource operations.
func executeCatchExecution(ctx EvalContext, catch *runbookconfigs.Catch, execution *runbookconfigs.Execution, failedStep *runbookruntime.Step, failDiags tfdiags.Diagnostics) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics

	operations := execution.Operations
	if len(operations) == 0 {
		for _, t := range execution.InvokeAction {
			operations = append(operations, runbookconfigs.ExecuteOperation{
				Type:      runbookconfigs.ExecuteOpInvokeAction,
				Traversal: t,
			})
		}
	}

	for _, operation := range operations {
		switch operation.Type {
		case runbookconfigs.ExecuteOpInvokeAction:
			diags = diags.Append(executeCatchInvokeAction(ctx, catch, operation, failedStep, failDiags))
		case runbookconfigs.ExecuteOpReadDataSource:
			diags = diags.Append(executeCatchReadDataSource(ctx, catch, operation, failedStep, failDiags))
		case runbookconfigs.ExecuteOpWait:
			// Wait operations in catch blocks: could be supported but
			// we'll defer for now — catches should be fast
			diags = diags.Append(tfdiags.Sourceless(
				tfdiags.Warning,
				"Wait not supported in catch blocks",
				"Wait operations inside catch blocks are not yet supported. The wait was skipped.",
			))
		}
		if diags.HasErrors() {
			return diags
		}
	}

	return diags
}

// executeCatchInvokeAction invokes an action within a catch block.
func executeCatchInvokeAction(ctx EvalContext, catch *runbookconfigs.Catch, operation runbookconfigs.ExecuteOperation, failedStep *runbookruntime.Step, failDiags tfdiags.Diagnostics) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics

	traversal := operation.Traversal
	ref, refDiags := parseActionRef(traversal)
	if refDiags.HasErrors() || ref == nil {
		return refDiags
	}

	// Find the action in the catch block's declared actions
	action := findCatchAction(catch, ref.actionType, ref.actionName)
	if action == nil {
		return diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Action not found",
			"The action "+ref.actionType+"."+ref.actionName+" is not declared in catch block \""+catch.Name+"\".",
		))
	}

	// Get provider and invoke
	providerType := providerTypeForAction(ctx.Config(), action)
	provider, provDiags := runbookProviderFresh(ctx, providerType)
	diags = diags.Append(provDiags)
	if provDiags.HasErrors() {
		return diags
	}

	// Evaluate config
	schemaResp := provider.GetProviderSchema()
	actionSchema := schemaResp.Actions[action.Type]
	configVal := cty.EmptyObjectVal
	if action.Config != nil && actionSchema.ConfigSchema != nil {
		value, _, valueDiags := ctx.EvaluateCatchBlock(failedStep, failDiags, action.Config, actionSchema.ConfigSchema)
		diags = diags.Append(valueDiags)
		if valueDiags.HasErrors() {
			return diags
		}
		configVal = value
	}
	if actionSchema.ConfigSchema != nil && (isEmptyObjectValue(configVal) || !configVal.Type().Equals(actionSchema.ConfigSchema.ImpliedType())) {
		coerced, err := actionSchema.ConfigSchema.CoerceValue(configVal)
		if err != nil {
			return diags.Append(err)
		}
		configVal = coerced
	}

	// Plan
	planResp := provider.PlanAction(providers.PlanActionRequest{ActionType: action.Type, ProposedActionData: configVal})
	diags = diags.Append(planResp.Diagnostics)
	if planResp.Diagnostics.HasErrors() {
		return diags
	}

	// Invoke. Surface the action's events and completion diagnostics the same way
	// the normal step path does (node_step_inner.go executeInvokeAction), so a
	// catch action's output is visible. Print-style actions deliver their entire
	// observable effect through these events, so draining them made the catch-all
	// appear to "not trigger" its action (hc-terraform-81y).
	subject := ref.actionType + "." + ref.actionName
	catchStepName := "catch." + catch.Name
	ctx.EmitActionEvent(ActionExecEvent{StepName: catchStepName, Subject: subject, ActionType: action.Type, Status: "running"})
	resp := provider.InvokeAction(providers.InvokeActionRequest{ActionType: action.Type, PlannedActionData: configVal})
	diags = diags.Append(resp.Diagnostics)
	if resp.Events != nil {
		for event := range resp.Events {
			switch e := event.(type) {
			case providers.InvokeActionEvent_Progress:
				ctx.EmitActionEvent(ActionExecEvent{StepName: catchStepName, Subject: subject, ActionType: action.Type, Status: "progress", Message: e.Message})
			case providers.InvokeActionEvent_Completed:
				ctx.EmitActionEvent(ActionExecEvent{StepName: catchStepName, Subject: subject, ActionType: action.Type, Status: "completed"})
				diags = diags.Append(e.Diagnostics)
			}
		}
	}

	return diags
}

// executeCatchReadDataSource reads a data source within a catch block.
func executeCatchReadDataSource(ctx EvalContext, catch *runbookconfigs.Catch, operation runbookconfigs.ExecuteOperation, failedStep *runbookruntime.Step, failDiags tfdiags.Diagnostics) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics

	traversal := operation.Traversal
	ref, refDiags := parseDataRef(traversal)
	if refDiags.HasErrors() || ref == nil {
		return refDiags
	}

	// Find data source in catch
	dataConfig := findCatchDataSource(catch, ref.dataType, ref.dataName)
	if dataConfig == nil {
		return diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Data source not found",
			"The data source "+ref.dataType+"."+ref.dataName+" is not declared in catch block \""+catch.Name+"\".",
		))
	}

	providerType := providerTypeForResource(ctx.Config(), dataConfig)
	provider, provDiags := runbookProviderFresh(ctx, providerType)
	diags = diags.Append(provDiags)
	if provDiags.HasErrors() {
		return diags
	}

	schemaResp := provider.GetProviderSchema()
	resourceSchema := schemaResp.SchemaForResourceAddr(dataConfig.Addr())
	configVal := cty.EmptyObjectVal
	providerMetaVal := cty.EmptyObjectVal
	if schemaResp.ProviderMeta.Body != nil {
		providerMetaVal = schemaResp.ProviderMeta.Body.EmptyValue()
	}
	if resourceSchema.Body != nil {
		value, _, valueDiags := ctx.EvaluateCatchBlock(failedStep, failDiags, dataConfig.Config, resourceSchema.Body)
		diags = diags.Append(valueDiags)
		if valueDiags.HasErrors() {
			return diags
		}
		configVal = value
	}

	resp := provider.ReadDataSource(providers.ReadDataSourceRequest{
		TypeName:     dataConfig.Type,
		Config:       configVal,
		ProviderMeta: providerMetaVal,
	})
	diags = diags.Append(resp.Diagnostics)
	// Note: data source results in catch blocks are available via outputs
	// but not stored in step eval state (catches are out-of-band)
	return diags
}

// --- helpers ---

type actionRefResult struct {
	actionType string
	actionName string
}

func parseActionRef(traversal hcl.Traversal) (*actionRefResult, tfdiags.Diagnostics) {
	if len(traversal) < 3 {
		return nil, tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
			tfdiags.Error, "Invalid action reference",
			"Expected action.<type>.<name> traversal.",
		))
	}
	root := traversal[0].(hcl.TraverseRoot)
	if root.Name != "action" {
		return nil, tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
			tfdiags.Error, "Invalid action reference",
			"Expected reference to start with 'action'.",
		))
	}
	typeAttr, ok := traversal[1].(hcl.TraverseAttr)
	if !ok {
		return nil, tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
			tfdiags.Error, "Invalid action reference",
			"Expected action type name.",
		))
	}
	nameAttr, ok := traversal[2].(hcl.TraverseAttr)
	if !ok {
		return nil, tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
			tfdiags.Error, "Invalid action reference",
			"Expected action instance name.",
		))
	}
	return &actionRefResult{actionType: typeAttr.Name, actionName: nameAttr.Name}, nil
}

type dataRefResult struct {
	dataType string
	dataName string
}

func parseDataRef(traversal hcl.Traversal) (*dataRefResult, tfdiags.Diagnostics) {
	if len(traversal) < 3 {
		return nil, tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
			tfdiags.Error, "Invalid data reference",
			"Expected data.<type>.<name> traversal.",
		))
	}
	root := traversal[0].(hcl.TraverseRoot)
	if root.Name != "data" {
		return nil, tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
			tfdiags.Error, "Invalid data reference",
			"Expected reference to start with 'data'.",
		))
	}
	typeAttr, ok := traversal[1].(hcl.TraverseAttr)
	if !ok {
		return nil, tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
			tfdiags.Error, "Invalid data reference",
			"Expected data source type name.",
		))
	}
	nameAttr, ok := traversal[2].(hcl.TraverseAttr)
	if !ok {
		return nil, tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
			tfdiags.Error, "Invalid data reference",
			"Expected data source instance name.",
		))
	}
	return &dataRefResult{dataType: typeAttr.Name, dataName: nameAttr.Name}, nil
}

func findCatchAction(catch *runbookconfigs.Catch, typ, name string) *configs.Action {
	for _, action := range catch.Actions {
		if action != nil && action.Type == typ && action.Name == name {
			return action
		}
	}
	return nil
}

func findCatchDataSource(catch *runbookconfigs.Catch, typ, name string) *configs.Resource {
	for _, ds := range catch.DataSources {
		if ds != nil && ds.Type == typ && ds.Name == name {
			return ds
		}
	}
	return nil
}

func sortedCatches(catches map[string]*runbookconfigs.Catch) []*runbookconfigs.Catch {
	names := make([]string, 0, len(catches))
	for name := range catches {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]*runbookconfigs.Catch, 0, len(names))
	for _, name := range names {
		result = append(result, catches[name])
	}
	return result
}

// SortedCatchNames returns catch block names in sorted order for deterministic output.
func SortedCatchNames(config *runbookconfigs.RunbookConfig) []string {
	if config == nil || len(config.Catches) == 0 {
		return nil
	}
	names := make([]string, 0, len(config.Catches))
	for name := range config.Catches {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// --- UI emission helpers ---

func emitCatchTriggered(ctx EvalContext, catchName, failedStepName string) {
	builtinCtx, ok := ctx.(*BuiltinEvalContext)
	if !ok {
		return
	}
	if builtinCtx.ui != nil {
		builtinCtx.ui.CatchTriggered(catchName, failedStepName)
	}
	for _, hook := range builtinCtx.hooks {
		hook.CatchTriggered(catchName, failedStepName)
	}
}

func emitCatchCompleted(ctx EvalContext, catchName, failedStepName string) {
	builtinCtx, ok := ctx.(*BuiltinEvalContext)
	if !ok {
		return
	}
	if builtinCtx.ui != nil {
		builtinCtx.ui.CatchCompleted(catchName, failedStepName)
	}
	for _, hook := range builtinCtx.hooks {
		hook.CatchCompleted(catchName, failedStepName)
	}
}

func emitCatchSkipped(ctx EvalContext, catchName, reason string) {
	builtinCtx, ok := ctx.(*BuiltinEvalContext)
	if !ok {
		return
	}
	if builtinCtx.ui != nil {
		builtinCtx.ui.CatchSkipped(catchName, reason)
	}
	for _, hook := range builtinCtx.hooks {
		hook.CatchSkipped(catchName, reason)
	}
}

func emitCatchFailed(ctx EvalContext, catchName, err string) {
	builtinCtx, ok := ctx.(*BuiltinEvalContext)
	if !ok {
		return
	}
	if builtinCtx.ui != nil {
		builtinCtx.ui.CatchFailed(catchName, err)
	}
	for _, hook := range builtinCtx.hooks {
		hook.CatchFailed(catchName, err)
	}
}
