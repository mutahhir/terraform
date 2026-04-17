// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"sort"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/dynblock"
	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform/internal/addrs"
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/lang"
	"github.com/hashicorp/terraform/internal/lang/blocktoattr"
	"github.com/hashicorp/terraform/internal/providers"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/states"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

// EvalContext tracks the values that are available while evaluating a runbook.
type EvalContext struct {
	config         *runbookconfigs.RunbookConfig
	workspaceState *states.State
	ui             UI
	hooks          []Hook
	emitLock       sync.Mutex

	variables     terraform.InputValues
	variablesLock sync.RWMutex

	providers     map[addrs.Provider]providers.Interface
	providersLock sync.RWMutex

	steps     map[string]*stepEvalState
	stepOrder []string
	stepsLock sync.RWMutex
}

type EvalContextOpts struct {
	Config         *runbookconfigs.RunbookConfig
	WorkspaceState *states.State
	UI             UI
	Hooks          []Hook
}

func NewEvalContext(opts EvalContextOpts) *EvalContext {
	return &EvalContext{
		config:         opts.Config,
		workspaceState: opts.WorkspaceState,
		ui:             opts.UI,
		hooks:          append([]Hook(nil), opts.Hooks...),
		emitLock:       sync.Mutex{},
		variables:      make(terraform.InputValues),
		variablesLock:  sync.RWMutex{},
		providers:      make(map[addrs.Provider]providers.Interface),
		providersLock:  sync.RWMutex{},
		steps:          make(map[string]*stepEvalState),
		stepOrder:      make([]string, 0),
		stepsLock:      sync.RWMutex{},
	}
}

func stepStateKey(name string, instanceKey addrs.InstanceKey) string {
	return runbookaddrs.StepInstance{StepName: name, InstanceKey: instanceKey}.String()
}

func parseStepStateKey(key string) runbookaddrs.StepInstance {
	if key == "" {
		return runbookaddrs.StepInstance{}
	}
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte(key), "", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return runbookaddrs.StepInstance{StepName: key}
	}
	root, ok := traversal[0].(hcl.TraverseRoot)
	if !ok {
		return runbookaddrs.StepInstance{StepName: key}
	}
	instance := runbookaddrs.StepInstance{StepName: root.Name, InstanceKey: terraformaddrs.NoKey}
	if len(traversal) > 1 {
		if idx, ok := traversal[1].(hcl.TraverseIndex); ok {
			parsed, err := terraformaddrs.ParseInstanceKey(idx.Key)
			if err == nil {
				instance.InstanceKey = parsed
			}
		}
	}
	return instance
}

func defaultStepStateKey(name string) string {
	return stepStateKey(name, addrs.NoKey)
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
	ec.emitLock.Lock()
	defer ec.emitLock.Unlock()
	snapshot := cloneRuntimeStepValue(step)
	if ec.ui != nil {
		ec.ui.PlannedStep(snapshot)
	}
	for _, hook := range ec.hooks {
		hook.PlannedStep(snapshot)
	}
}

func (ec *EvalContext) EmitStepPlanInfo(info StepPlanInfo) {
	ec.emitLock.Lock()
	defer ec.emitLock.Unlock()
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
	planned       bool
	invoked       bool
	plannedConfig cty.Value
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

func (ec *EvalContext) WorkspaceState() *states.State {
	return ec.workspaceState
}

func (ec *EvalContext) SetVariable(name string, value *terraform.InputValue) {
	ec.variablesLock.Lock()
	defer ec.variablesLock.Unlock()

	ec.variables[name] = value
}

func (ec *EvalContext) GetVariable(name string) (*terraform.InputValue, bool) {
	ec.variablesLock.RLock()
	defer ec.variablesLock.RUnlock()

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
	ec.providersLock.RLock()
	defer ec.providersLock.RUnlock()

	provider, ok := ec.providers[providerType]
	return provider, ok
}

func (ec *EvalContext) EnsureStep(name string, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step {
	return ec.ensureStepWithKey(name, addrs.NoKey, config, existing)
}

func (ec *EvalContext) ensureStepWithKey(name string, instanceKey addrs.InstanceKey, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()
	return ec.ensureStepRuntimeLocked(name, instanceKey, config, existing)
}

func (ec *EvalContext) ensurePlannedStepWithKey(name string, instanceKey addrs.InstanceKey, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()
	step := ec.ensureStepRuntimeLocked(name, instanceKey, config, existing)
	if step.Status == runbookruntime.StepStatusPending {
		step.Status = runbookruntime.StepStatusPlanned
	}
	return step
}

func (ec *EvalContext) ensureRunningStepWithKey(name string, instanceKey addrs.InstanceKey, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()
	step := ec.ensureStepRuntimeLocked(name, instanceKey, config, existing)
	if step.Status == runbookruntime.StepStatusPending || step.Status == runbookruntime.StepStatusPlanned {
		step.Status = runbookruntime.StepStatusRunning
	}
	return step
}

func (ec *EvalContext) ensureStepRuntimeLocked(name string, instanceKey addrs.InstanceKey, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step {
	key := stepStateKey(name, instanceKey)
	state := ec.ensureStepStateLocked(key)
	if state.runtime == nil {
		if existing != nil {
			state.runtime = existing
		} else {
			state.runtime = &runbookruntime.Step{
				Config:      config,
				Name:        name,
				Index:       0,
				InstanceKey: instanceKey,
			}
		}
	}
	if existing != nil {
		state.runtime.Name = existing.Name
		state.runtime.Index = existing.Index
		if existing.Config != nil {
			state.runtime.Config = existing.Config
		}
		if existing.InstanceKey != nil {
			state.runtime.InstanceKey = existing.InstanceKey
		}
		state.runtime.RepetitionData = existing.RepetitionData
	}
	if state.runtime.Config == nil {
		state.runtime.Config = config
	}
	if state.runtime.Name == "" {
		state.runtime.Name = name
	}
	if state.runtime.InstanceKey == nil {
		state.runtime.InstanceKey = instanceKey
	}
	if state.runtime.RepetitionData == nil && existing != nil {
		state.runtime.RepetitionData = existing.RepetitionData
	}
	if state.runtime.Outputs == cty.NilVal && len(state.outputs) > 0 {
		state.runtime.Outputs = cty.ObjectVal(copyValueMap(state.outputs))
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
	return ec.stepWithKey(name, addrs.NoKey)
}

func (ec *EvalContext) stepWithKey(name string, instanceKey addrs.InstanceKey) (*runbookruntime.Step, bool) {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()

	state, ok := ec.steps[stepStateKey(name, instanceKey)]
	if !ok || state.runtime == nil {
		return nil, false
	}
	return cloneRuntimeStepValue(state.runtime), true
}

func (ec *EvalContext) StepsInOrder() []*runbookruntime.Step {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()

	steps := make([]*runbookruntime.Step, 0, len(ec.stepOrder))
	for _, key := range ec.stepOrder {
		state := ec.steps[key]
		if state != nil && state.runtime != nil {
			steps = append(steps, cloneRuntimeStepValue(state.runtime))
		}
	}
	return steps
}

func (ec *EvalContext) SetStepStatus(name string, status runbookruntime.StepStatus, reason string) {
	ec.setStepStatusWithKey(name, addrs.NoKey, status, reason)
}

func (ec *EvalContext) setStepStatusWithKey(name string, instanceKey addrs.InstanceKey, status runbookruntime.StepStatus, reason string) {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	key := stepStateKey(name, instanceKey)
	state := ec.ensureStepStateLocked(key)
	if state.runtime == nil {
		state.runtime = &runbookruntime.Step{Name: name, InstanceKey: instanceKey}
	}
	state.runtime.Status = status
	if reason != "" {
		state.runtime.SkipReason = reason
	}
}

func (ec *EvalContext) SetStepOutput(stepName, outputName string, value cty.Value) {
	ec.setStepOutputWithKey(stepName, addrs.NoKey, outputName, value)
}

func (ec *EvalContext) setStepOutputWithKey(stepName string, instanceKey addrs.InstanceKey, outputName string, value cty.Value) {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	key := stepStateKey(stepName, instanceKey)
	state := ec.ensureStepStateLocked(key)
	if state.runtime == nil {
		state.runtime = &runbookruntime.Step{Name: stepName, InstanceKey: instanceKey}
	}
	state.outputs[outputName] = value
	state.runtime.Outputs = setObjectAttr(state.runtime.Outputs, outputName, value)
}

func (ec *EvalContext) StepOutput(stepName, outputName string) (cty.Value, bool) {
	return ec.stepOutputWithKey(stepName, addrs.NoKey, outputName)
}

func (ec *EvalContext) stepOutputWithKey(stepName string, instanceKey addrs.InstanceKey, outputName string) (cty.Value, bool) {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()

	state, ok := ec.steps[stepStateKey(stepName, instanceKey)]
	if !ok {
		return cty.NilVal, false
	}
	value, ok := state.outputs[outputName]
	return value, ok
}

func (ec *EvalContext) SetStepLocal(stepName, localName string, value cty.Value) {
	ec.setStepValueWithKey(stepName, addrs.NoKey, func(state *stepEvalState) {
		state.locals[localName] = value
	})
}

func (ec *EvalContext) SetStepData(stepName string, addr addrs.Resource, value cty.Value) {
	ec.setStepValueWithKey(stepName, addrs.NoKey, func(state *stepEvalState) {
		state.data[addr.String()] = value
	})
}

func (ec *EvalContext) SetStepList(stepName string, addr addrs.Resource, value cty.Value) {
	ec.setStepValueWithKey(stepName, addrs.NoKey, func(state *stepEvalState) {
		state.lists[addr.String()] = value
	})
}

func (ec *EvalContext) MarkActionPlanned(stepName string, addr addrs.Action) {
	ec.setStepValueWithKey(stepName, addrs.NoKey, func(state *stepEvalState) {
		actionState, ok := state.actions[addr.String()]
		if !ok {
			actionState = &actionEvalState{}
			state.actions[addr.String()] = actionState
		}
		actionState.planned = true
	})
}

func (ec *EvalContext) setActionPlannedWithKey(stepName string, instanceKey addrs.InstanceKey, addr addrs.Action, config cty.Value) {
	ec.setStepValueWithKey(stepName, instanceKey, func(state *stepEvalState) {
		actionState, ok := state.actions[addr.String()]
		if !ok {
			actionState = &actionEvalState{}
			state.actions[addr.String()] = actionState
		}
		actionState.planned = true
		actionState.plannedConfig = config
	})
}

func (ec *EvalContext) actionPlannedConfigWithKey(stepName string, instanceKey addrs.InstanceKey, addr addrs.Action) (cty.Value, bool) {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()
	state, ok := ec.steps[stepStateKey(stepName, instanceKey)]
	if !ok {
		return cty.NilVal, false
	}
	actionState, ok := state.actions[addr.String()]
	if !ok || actionState == nil || actionState.plannedConfig == cty.NilVal {
		return cty.NilVal, false
	}
	return actionState.plannedConfig, true
}

func (ec *EvalContext) MarkActionInvoked(stepName string, addr addrs.Action) {
	ec.setStepValueWithKey(stepName, addrs.NoKey, func(state *stepEvalState) {
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

func (ec *EvalContext) stepHasStatusWithKey(name string, instanceKey addrs.InstanceKey, statuses ...runbookruntime.StepStatus) bool {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()
	state, ok := ec.steps[stepStateKey(name, instanceKey)]
	if !ok || state.runtime == nil {
		return false
	}
	for _, status := range statuses {
		if state.runtime.Status == status {
			return true
		}
	}
	return false
}

func (ec *EvalContext) setStepValue(stepName string, apply func(state *stepEvalState)) {
	ec.setStepValueWithKey(stepName, addrs.NoKey, apply)
}

func (ec *EvalContext) setStepValueWithKey(stepName string, instanceKey addrs.InstanceKey, apply func(state *stepEvalState)) {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	key := stepStateKey(stepName, instanceKey)
	state := ec.ensureStepStateLocked(key)
	apply(state)
}

func (ec *EvalContext) ensureStepStateLocked(key string) *stepEvalState {
	state, ok := ec.steps[key]
	if ok {
		return state
	}
	state = &stepEvalState{
		locals:  make(map[string]cty.Value),
		data:    make(map[string]cty.Value),
		lists:   make(map[string]cty.Value),
		actions: make(map[string]*actionEvalState),
		outputs: make(map[string]cty.Value),
	}
	ec.steps[key] = state
	ec.stepOrder = append(ec.stepOrder, key)
	return state
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
	return ec.stepLocalWithKey(stepName, addrs.NoKey, localName)
}

func (ec *EvalContext) stepLocalWithKey(stepName string, instanceKey addrs.InstanceKey, localName string) (cty.Value, bool) {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()

	state, ok := ec.steps[stepStateKey(stepName, instanceKey)]
	if !ok {
		return cty.NilVal, false
	}
	value, ok := state.locals[localName]
	return value, ok
}

func (ec *EvalContext) StepData(stepName string, addr addrs.Resource) (cty.Value, bool) {
	return ec.stepDataWithKey(stepName, addrs.NoKey, addr)
}

func (ec *EvalContext) stepDataWithKey(stepName string, instanceKey addrs.InstanceKey, addr addrs.Resource) (cty.Value, bool) {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()

	state, ok := ec.steps[stepStateKey(stepName, instanceKey)]
	if !ok {
		return cty.NilVal, false
	}
	value, ok := state.data[addr.String()]
	return value, ok
}

func (ec *EvalContext) StepList(stepName string, addr addrs.Resource) (cty.Value, bool) {
	return ec.stepListWithKey(stepName, addrs.NoKey, addr)
}

func (ec *EvalContext) stepListWithKey(stepName string, instanceKey addrs.InstanceKey, addr addrs.Resource) (cty.Value, bool) {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()

	state, ok := ec.steps[stepStateKey(stepName, instanceKey)]
	if !ok {
		return cty.NilVal, false
	}
	value, ok := state.lists[addr.String()]
	return value, ok
}

func (ec *EvalContext) EvaluateExpr(stepName string, expr hcl.Expression) (cty.Value, tfdiags.Diagnostics) {
	return ec.EvaluateExprForInstance(stepName, terraformaddrs.NoKey, nil, expr)
}

func (ec *EvalContext) EvaluateExprForInstance(stepName string, instanceKey terraformaddrs.InstanceKey, repetitionData *terraform.InstanceKeyEvalData, expr hcl.Expression) (cty.Value, tfdiags.Diagnostics) {
	if expr == nil {
		return cty.NilVal, nil
	}
	scope := &lang.Scope{BaseDir: ".", PureOnly: true}
	hclCtx := &hcl.EvalContext{
		Variables: ec.expressionVariablesForInstance(stepName, instanceKey, repetitionData),
		Functions: scope.Functions(),
	}
	value, hclDiags := expr.Value(hclCtx)
	return value, tfdiags.Diagnostics{}.Append(hclDiags)
}

func (ec *EvalContext) expressionVariables(stepName string) map[string]cty.Value {
	return ec.expressionVariablesForInstance(stepName, terraformaddrs.NoKey, nil)
}

func (ec *EvalContext) expressionVariablesForInstance(stepName string, instanceKey terraformaddrs.InstanceKey, repetitionData *terraform.InstanceKeyEvalData) map[string]cty.Value {
	variables := map[string]cty.Value{}

	varAttrs := map[string]cty.Value{}
	ec.variablesLock.RLock()
	for name, value := range ec.variables {
		if value != nil && value.Value != cty.NilVal {
			varAttrs[name] = value.Value
		}
	}
	ec.variablesLock.RUnlock()
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

	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()

	if state, ok := ec.steps[stepStateKey(stepName, instanceKey)]; ok {
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

	if repetitionData != nil {
		countAttrs := map[string]cty.Value{}
		if repetitionData.CountIndex != cty.NilVal {
			countAttrs["index"] = repetitionData.CountIndex
		}
		variables["count"] = cty.ObjectVal(countAttrs)

		eachAttrs := map[string]cty.Value{}
		if repetitionData.EachKey != cty.NilVal {
			eachAttrs["key"] = repetitionData.EachKey
		}
		if repetitionData.EachValue != cty.NilVal {
			eachAttrs["value"] = repetitionData.EachValue
		}
		variables["each"] = cty.ObjectVal(eachAttrs)
	}

	type stepGroupEntry struct {
		instance runbookaddrs.StepInstance
		state    *stepEvalState
	}
	stepGroups := map[string][]stepGroupEntry{}
	for key, state := range ec.steps {
		if state == nil {
			continue
		}
		instance := parseStepStateKey(key)
		if instance.StepName == "" {
			continue
		}
		stepGroups[instance.StepName] = append(stepGroups[instance.StepName], stepGroupEntry{instance: instance, state: state})
	}
	stepAttrs := map[string]cty.Value{}
	for name, entries := range stepGroups {
		if len(entries) == 1 && entries[0].instance.InstanceKey == terraformaddrs.NoKey {
			outputs := map[string]cty.Value{}
			for outputName, value := range entries[0].state.outputs {
				outputs[outputName] = value
			}
			if len(outputs) == 0 {
				stepAttrs[name] = cty.EmptyObjectVal
				continue
			}
			stepAttrs[name] = cty.ObjectVal(copyValueMap(outputs))
			continue
		}

		instances := map[string]cty.Value{}
		for _, entry := range entries {
			key := instanceObjectKey(entry.instance.InstanceKey)
			if key == "" {
				key = "default"
			}
			outputs := map[string]cty.Value{}
			for outputName, value := range entry.state.outputs {
				outputs[outputName] = value
			}
			if len(outputs) == 0 {
				instances[key] = cty.EmptyObjectVal
				continue
			}
			instances[key] = cty.ObjectVal(copyValueMap(outputs))
		}
		if len(instances) == 0 {
			stepAttrs[name] = cty.EmptyObjectVal
			continue
		}
		stepAttrs[name] = cty.ObjectVal(copyValueMap(instances))
	}
	stepVals := cty.ObjectVal(stepAttrs)
	variables["step"] = stepVals
	variables["steps"] = stepVals
	variables["workspace"] = ec.workspaceVariables()

	return variables
}

func instanceObjectKey(key terraformaddrs.InstanceKey) string {
	switch k := key.(type) {
	case nil:
		return ""
	case terraformaddrs.StringKey:
		return string(k)
	case terraformaddrs.IntKey:
		return k.Value().AsBigFloat().Text('f', 0)
	default:
		return key.String()
	}
}

func (ec *EvalContext) workspaceVariables() cty.Value {
	config := ec.WorkspaceConfig()
	if config == nil || config.Module == nil {
		return cty.EmptyObjectVal
	}
	return ec.workspaceModuleValue(config, config.Module)
}

func (ec *EvalContext) workspaceModuleValue(config *configs.Config, module *configs.Module) cty.Value {
	attrs := map[string]cty.Value{}

	outputs := map[string]cty.Value{}
	for name, output := range module.Outputs {
		if output == nil {
			continue
		}
		outputs[name] = cty.DynamicVal
	}
	attrs["output"] = cty.ObjectVal(outputs)

	actions := map[string]map[string]cty.Value{}
	for key, action := range module.Actions {
		if action == nil {
			continue
		}
		if actions[action.Type] == nil {
			actions[action.Type] = map[string]cty.Value{}
		}
		actions[action.Type][action.Name] = cty.StringVal(key)
	}
	attrs["action"] = nestedObjectValue(actions)
	attrs["data"] = cty.EmptyObjectVal

	children := map[string]cty.Value{}
	for name, child := range config.Children {
		if child == nil || child.Module == nil {
			continue
		}
		children[name] = ec.workspaceModuleValue(child, child.Module)
	}
	attrs["module"] = cty.ObjectVal(copyValueMap(children))

	return cty.ObjectVal(attrs)
}

func nestedObjectValue(src map[string]map[string]cty.Value) cty.Value {
	if len(src) == 0 {
		return cty.EmptyObjectVal
	}
	outer := make(map[string]cty.Value, len(src))
	for key, values := range src {
		outer[key] = cty.ObjectVal(copyValueMap(values))
	}
	return cty.ObjectVal(outer)
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
	return ec.EvaluateBlockForInstance("", terraformaddrs.NoKey, nil, body, schema)
}

func (ec *EvalContext) EvaluateBlockForInstance(stepName string, instanceKey terraformaddrs.InstanceKey, repetitionData *terraform.InstanceKeyEvalData, body hcl.Body, schema *configschema.Block) (cty.Value, hcl.Body, tfdiags.Diagnostics) {
	if schema == nil {
		return cty.EmptyObjectVal, body, nil
	}
	if body == nil {
		return schema.EmptyValue(), nil, nil
	}

	funcs := (&lang.Scope{BaseDir: ".", PureOnly: true, ForProvider: true}).Functions()
	hclCtx := &hcl.EvalContext{
		Variables: ec.expressionVariablesForInstance(stepName, instanceKey, repetitionData),
		Functions: funcs,
	}
	var diags tfdiags.Diagnostics
	expandedBody := dynblock.Expand(body, hclCtx)
	fixedBody := blocktoattr.FixUpBlockAttrs(expandedBody, schema)
	val, evalDiags := hcldec.Decode(fixedBody, schema.DecoderSpec(), hclCtx)
	diags = diags.Append(evalDiags)
	return val, fixedBody, diags
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

func cloneRuntimeStepValue(step *runbookruntime.Step) *runbookruntime.Step {
	if step == nil {
		return nil
	}
	copy := *step
	if step.RepetitionData != nil {
		repetitionCopy := *step.RepetitionData
		copy.RepetitionData = &repetitionCopy
	}
	return &copy
}
