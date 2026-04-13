// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"sort"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform/internal/addrs"
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/lang"
	"github.com/hashicorp/terraform/internal/providers"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

// EvalContext tracks the values that are available while evaluating a runbook.
type EvalContext struct {
	config *runbookconfigs.RunbookConfig
	ui     UI
	hooks  []Hook

	variables     terraform.InputValues
	variablesLock sync.Mutex

	providers     map[addrs.Provider]providers.Interface
	providersLock sync.Mutex

	steps     map[string]*stepEvalState
	stepOrder []string
	stepsLock sync.Mutex
}

type EvalContextOpts struct {
	Config *runbookconfigs.RunbookConfig
	UI     UI
	Hooks  []Hook
}

func NewEvalContext(opts EvalContextOpts) *EvalContext {
	return &EvalContext{
		config:        opts.Config,
		ui:            opts.UI,
		hooks:         append([]Hook(nil), opts.Hooks...),
		variables:     make(terraform.InputValues),
		variablesLock: sync.Mutex{},
		providers:     make(map[addrs.Provider]providers.Interface),
		providersLock: sync.Mutex{},
		steps:         make(map[string]*stepEvalState),
		stepOrder:     make([]string, 0),
		stepsLock:     sync.Mutex{},
	}
}

func (ec *EvalContext) UI() UI {
	return ec.ui
}

func (ec *EvalContext) Hooks() []Hook {
	return ec.hooks
}

func (ec *EvalContext) EmitPlannedStep(step *runbookruntime.Step) {
	if step == nil {
		return
	}
	if ec.ui != nil {
		ec.ui.PlannedStep(step)
	}
	for _, hook := range ec.hooks {
		hook.PlannedStep(step)
	}
}

func (ec *EvalContext) EmitStepPlanInfo(info StepPlanInfo) {
	if ec.ui != nil {
		ec.ui.PlannedStepInfo(info)
	}
	for _, hook := range ec.hooks {
		hook.PlannedStepInfo(info)
	}
}

type stepEvalState struct {
	runtime *runbookruntime.Step

	locals  map[string]cty.Value
	data    map[string]cty.Value
	lists   map[string]cty.Value
	actions map[string]*actionEvalState
	outputs map[string]cty.Value
}

type actionEvalState struct {
	planned bool
	invoked bool
}

func (ec *EvalContext) Config() *runbookconfigs.RunbookConfig {
	return ec.config
}

func (ec *EvalContext) WorkspaceConfig() *configs.Config {
	if ec.config == nil {
		return nil
	}
	return ec.config.WorkspaceConfig
}

func (ec *EvalContext) SetVariable(name string, value *terraform.InputValue) {
	ec.variablesLock.Lock()
	defer ec.variablesLock.Unlock()

	ec.variables[name] = value
}

func (ec *EvalContext) GetVariable(name string) (*terraform.InputValue, bool) {
	ec.variablesLock.Lock()
	defer ec.variablesLock.Unlock()

	value, ok := ec.variables[name]
	return value, ok
}

func (ec *EvalContext) ProviderInput(addrs.AbsProviderConfig) map[string]cty.Value {
	return nil
}

func (ec *EvalContext) SetProvider(providerType addrs.Provider, provider providers.Interface) {
	ec.providersLock.Lock()
	defer ec.providersLock.Unlock()

	ec.providers[providerType] = provider
}

func (ec *EvalContext) Provider(providerType addrs.Provider) (providers.Interface, bool) {
	ec.providersLock.Lock()
	defer ec.providersLock.Unlock()

	provider, ok := ec.providers[providerType]
	return provider, ok
}

func (ec *EvalContext) EnsureStep(name string, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	state, ok := ec.steps[name]
	if !ok {
		state = &stepEvalState{
			locals:  make(map[string]cty.Value),
			data:    make(map[string]cty.Value),
			lists:   make(map[string]cty.Value),
			actions: make(map[string]*actionEvalState),
			outputs: make(map[string]cty.Value),
		}
		ec.steps[name] = state
		ec.stepOrder = append(ec.stepOrder, name)
	}
	if state.runtime != nil {
		return state.runtime
	}

	if existing != nil {
		state.runtime = existing
	} else {
		state.runtime = &runbookruntime.Step{
			Config: config,
			Name:   name,
			Index:  0,
		}
	}
	if state.runtime.Config == nil {
		state.runtime.Config = config
	}
	if state.runtime.Outputs == cty.NilVal {
		state.runtime.Outputs = cty.NilVal
	}
	if state.runtime.Status == "" {
		state.runtime.Status = runbookruntime.StepStatusPending
	}
	return state.runtime
}

func (ec *EvalContext) Step(name string) (*runbookruntime.Step, bool) {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	state, ok := ec.steps[name]
	if !ok || state.runtime == nil {
		return nil, false
	}
	return state.runtime, true
}

func (ec *EvalContext) StepsInOrder() []*runbookruntime.Step {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	steps := make([]*runbookruntime.Step, 0, len(ec.stepOrder))
	for _, name := range ec.stepOrder {
		state := ec.steps[name]
		if state != nil && state.runtime != nil {
			steps = append(steps, state.runtime)
		}
	}
	return steps
}

func (ec *EvalContext) SetStepStatus(name string, status runbookruntime.StepStatus, reason string) {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	state, ok := ec.steps[name]
	if !ok || state.runtime == nil {
		return
	}
	state.runtime.Status = status
	if reason != "" {
		state.runtime.SkipReason = reason
	}
}

func (ec *EvalContext) SetStepOutput(stepName, outputName string, value cty.Value) {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	state, ok := ec.steps[stepName]
	if !ok {
		state = &stepEvalState{
			locals:  make(map[string]cty.Value),
			data:    make(map[string]cty.Value),
			lists:   make(map[string]cty.Value),
			actions: make(map[string]*actionEvalState),
			outputs: make(map[string]cty.Value),
		}
		ec.steps[stepName] = state
		ec.stepOrder = append(ec.stepOrder, stepName)
	}
	state.outputs[outputName] = value
	if state.runtime != nil {
		state.runtime.Outputs = setObjectAttr(state.runtime.Outputs, outputName, value)
	}
}

func (ec *EvalContext) StepOutput(stepName, outputName string) (cty.Value, bool) {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	state, ok := ec.steps[stepName]
	if !ok {
		return cty.NilVal, false
	}
	value, ok := state.outputs[outputName]
	return value, ok
}

func (ec *EvalContext) SetStepLocal(stepName, localName string, value cty.Value) {
	ec.setStepValue(stepName, func(state *stepEvalState) {
		state.locals[localName] = value
	})
}

func (ec *EvalContext) SetStepData(stepName string, addr addrs.Resource, value cty.Value) {
	ec.setStepValue(stepName, func(state *stepEvalState) {
		state.data[addr.String()] = value
	})
}

func (ec *EvalContext) SetStepList(stepName string, addr addrs.Resource, value cty.Value) {
	ec.setStepValue(stepName, func(state *stepEvalState) {
		state.lists[addr.String()] = value
	})
}

func (ec *EvalContext) MarkActionPlanned(stepName string, addr addrs.Action) {
	ec.setStepValue(stepName, func(state *stepEvalState) {
		actionState, ok := state.actions[addr.String()]
		if !ok {
			actionState = &actionEvalState{}
			state.actions[addr.String()] = actionState
		}
		actionState.planned = true
	})
}

func (ec *EvalContext) MarkActionInvoked(stepName string, addr addrs.Action) {
	ec.setStepValue(stepName, func(state *stepEvalState) {
		actionState, ok := state.actions[addr.String()]
		if !ok {
			actionState = &actionEvalState{}
			state.actions[addr.String()] = actionState
		}
		actionState.invoked = true
	})
}

func (ec *EvalContext) HasDependencyState(name string, statuses ...runbookruntime.StepStatus) bool {
	step, ok := ec.Step(name)
	if !ok {
		return false
	}
	for _, status := range statuses {
		if step.Status == status {
			return true
		}
	}
	return false
}

func (ec *EvalContext) setStepValue(stepName string, apply func(state *stepEvalState)) {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	state, ok := ec.steps[stepName]
	if !ok {
		state = &stepEvalState{
			locals:  make(map[string]cty.Value),
			data:    make(map[string]cty.Value),
			lists:   make(map[string]cty.Value),
			actions: make(map[string]*actionEvalState),
			outputs: make(map[string]cty.Value),
		}
		ec.steps[stepName] = state
		ec.stepOrder = append(ec.stepOrder, stepName)
	}
	apply(state)
}

func setObjectAttr(current cty.Value, name string, value cty.Value) cty.Value {
	attrs := map[string]cty.Value{}
	if current != cty.NilVal && current.IsKnown() && !current.IsNull() && current.Type().IsObjectType() {
		for attrName, attrValue := range current.AsValueMap() {
			attrs[attrName] = attrValue
		}
	}
	attrs[name] = value
	return cty.ObjectVal(attrs)
}

func (ec *EvalContext) StepLocal(stepName, localName string) (cty.Value, bool) {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	state, ok := ec.steps[stepName]
	if !ok {
		return cty.NilVal, false
	}
	value, ok := state.locals[localName]
	return value, ok
}

func (ec *EvalContext) StepData(stepName string, addr addrs.Resource) (cty.Value, bool) {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	state, ok := ec.steps[stepName]
	if !ok {
		return cty.NilVal, false
	}
	value, ok := state.data[addr.String()]
	return value, ok
}

func (ec *EvalContext) StepList(stepName string, addr addrs.Resource) (cty.Value, bool) {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	state, ok := ec.steps[stepName]
	if !ok {
		return cty.NilVal, false
	}
	value, ok := state.lists[addr.String()]
	return value, ok
}

func (ec *EvalContext) EvaluateExpr(stepName string, expr hcl.Expression) (cty.Value, tfdiags.Diagnostics) {
	if expr == nil {
		return cty.NilVal, nil
	}
	scope := &lang.Scope{BaseDir: ".", PureOnly: true}
	hclCtx := &hcl.EvalContext{
		Variables: ec.expressionVariables(stepName),
		Functions: scope.Functions(),
	}
	value, hclDiags := expr.Value(hclCtx)
	return value, tfdiags.Diagnostics{}.Append(hclDiags)
}

func (ec *EvalContext) expressionVariables(stepName string) map[string]cty.Value {
	variables := map[string]cty.Value{}

	varAttrs := map[string]cty.Value{}
	ec.variablesLock.Lock()
	for name, value := range ec.variables {
		if value != nil && value.Value != cty.NilVal {
			varAttrs[name] = value.Value
		}
	}
	ec.variablesLock.Unlock()
	if ec.config != nil {
		for name, variable := range ec.config.Variables {
			if _, exists := varAttrs[name]; exists {
				continue
			}
			if variable.Default != cty.NilVal {
				varAttrs[name] = variable.Default
				continue
			}
			if ty := variableValueType(variable); ty != cty.NilType {
				varAttrs[name] = cty.UnknownVal(ty)
				continue
			}
			varAttrs[name] = cty.DynamicVal
		}
	}
	variables["var"] = cty.ObjectVal(varAttrs)

	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	if state, ok := ec.steps[stepName]; ok {
		variables["local"] = cty.ObjectVal(copyValueMapOrEmpty(state.locals))
		dataVal := nestedResourceValues(state.data)
		if dataVal != cty.NilVal {
			variables["data"] = dataVal
		} else {
			variables["data"] = cty.EmptyObjectVal
		}
		listVal := nestedResourceValues(state.lists)
		if listVal != cty.NilVal {
			variables["list"] = listVal
		} else {
			variables["list"] = cty.EmptyObjectVal
		}
	} else {
		variables["local"] = cty.EmptyObjectVal
		variables["data"] = cty.EmptyObjectVal
		variables["list"] = cty.EmptyObjectVal
	}

	stepAttrs := map[string]cty.Value{}
	for name, state := range ec.steps {
		if state == nil {
			continue
		}
		if len(state.outputs) == 0 {
			stepAttrs[name] = cty.EmptyObjectVal
			continue
		}
		stepAttrs[name] = cty.ObjectVal(copyValueMap(state.outputs))
	}
	stepVals := cty.ObjectVal(stepAttrs)
	variables["step"] = stepVals
	variables["steps"] = stepVals

	return variables
}

func copyValueMap(src map[string]cty.Value) map[string]cty.Value {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]cty.Value, len(src))
	keys := make([]string, 0, len(src))
	for key := range src {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		dst[key] = src[key]
	}
	return dst
}

func copyValueMapOrEmpty(src map[string]cty.Value) map[string]cty.Value {
	if len(src) == 0 {
		return map[string]cty.Value{}
	}
	return copyValueMap(src)
}

func nestedResourceValues(src map[string]cty.Value) cty.Value {
	if len(src) == 0 {
		return cty.NilVal
	}
	byType := make(map[string]map[string]cty.Value)
	for addrStr, value := range src {
		traversal, hclDiags := hclsyntax.ParseTraversalAbs([]byte(addrStr), "", hcl.Pos{Line: 1, Column: 1})
		if hclDiags.HasErrors() {
			continue
		}
		ref, diags := runbookaddrs.ParseRef(traversal)
		if diags.HasErrors() || ref == nil {
			continue
		}
		resource, ok := ref.Subject.(addrs.Resource)
		if !ok {
			continue
		}
		if byType[resource.Type] == nil {
			byType[resource.Type] = make(map[string]cty.Value)
		}
		byType[resource.Type][resource.Name] = value
	}
	if len(byType) == 0 {
		return cty.NilVal
	}
	outer := make(map[string]cty.Value, len(byType))
	for typ, values := range byType {
		outer[typ] = cty.ObjectVal(copyValueMap(values))
	}
	return cty.ObjectVal(outer)
}

func (ec *EvalContext) EvaluateBlock(body hcl.Body, schema *configschema.Block) (cty.Value, hcl.Body, tfdiags.Diagnostics) {
	if schema == nil {
		return cty.EmptyObjectVal, body, nil
	}
	if body == nil {
		return schema.EmptyValue(), nil, nil
	}

	scope := &lang.Scope{Data: providerEvalData{ctx: ec}, ParseRef: terraformaddrs.ParseRef}
	var diags tfdiags.Diagnostics
	body, expandDiags := scope.ExpandBlock(body, schema)
	diags = diags.Append(expandDiags)
	val, evalDiags := scope.EvalBlock(body, schema)
	diags = diags.Append(evalDiags)
	return val, body, diags
}

func variableValueType(variable *configs.Variable) cty.Type {
	if variable == nil {
		return cty.NilType
	}
	if variable.ConstraintType != cty.NilType {
		return variable.ConstraintType
	}
	return variable.Type
}
