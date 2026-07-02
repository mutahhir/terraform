// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/dynblock"
	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/hcl/v2/hclsyntax"
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
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// BuiltinEvalContext tracks the values that are available while evaluating a runbook.
type BuiltinEvalContext struct {
	stopCtx           context.Context
	config            *runbookconfigs.RunbookConfig
	workspaceState    *states.State
	ui                UI
	hooks             []Hook
	emitLock          sync.Mutex
	workspaceReadLock sync.Mutex
	workspaceReads    map[string]struct{}
	planInfo          []StepPlanInfo

	variables     terraform.InputValues
	variablesLock sync.RWMutex

	providers          map[string]*providerEntry // keyed by AbsProviderConfig.String()
	providersLock      sync.RWMutex
	providerFactories  map[terraformaddrs.Provider]providers.Factory

	steps     map[string]*stepEvalState
	stepOrder []string
	stepsLock sync.RWMutex

	// Catch-all support. The failed step for catch evaluation is no longer held
	// here as a shared slot; it is threaded per catch-evaluation through the
	// EvaluateCatch* entry points so concurrent step failures cannot stomp each
	// other's failed_step (hc-terraform-wdc.4). catchOutputs (keyed by catch
	// name) remains global by design.
	catchOutputs map[string]map[string]cty.Value // catch name -> output name -> value
	catchLock    sync.RWMutex
}

type EvalContextOpts struct {
	StopCtx        context.Context
	Config         *runbookconfigs.RunbookConfig
	WorkspaceState *states.State
	UI             UI
	Hooks          []Hook
}

func NewEvalContext(opts EvalContextOpts) *BuiltinEvalContext {
	stopCtx := opts.StopCtx
	if stopCtx == nil {
		stopCtx = context.Background()
	}
	return &BuiltinEvalContext{
		stopCtx:           stopCtx,
		config:            opts.Config,
		workspaceState:    opts.WorkspaceState,
		ui:                opts.UI,
		hooks:             append([]Hook(nil), opts.Hooks...),
		emitLock:          sync.Mutex{},
		workspaceReadLock: sync.Mutex{},
		workspaceReads:    map[string]struct{}{},
		planInfo:          make([]StepPlanInfo, 0),
		variables:         make(terraform.InputValues),
		variablesLock:     sync.RWMutex{},
		providers:         make(map[string]*providerEntry),
		providersLock:     sync.RWMutex{},
		steps:             make(map[string]*stepEvalState),
		stepOrder:         make([]string, 0),
		stepsLock:         sync.RWMutex{},
	}
}

func stepStateKey(name string, instanceKey terraformaddrs.InstanceKey) string {
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
	return stepStateKey(name, terraformaddrs.NoKey)
}

func (ec *BuiltinEvalContext) StopCtx() context.Context {
	return ec.stopCtx
}

func (ec *BuiltinEvalContext) UI() UI {
	return ec.ui
}

func (ec *BuiltinEvalContext) Hooks() []Hook {
	return ec.hooks
}

func (ec *BuiltinEvalContext) EmitPlannedStep(step *runbookruntime.Step) (HookAction, error) {
	if step == nil {
		return HookActionContinue, nil
	}
	ec.emitLock.Lock()
	defer ec.emitLock.Unlock()
	snapshot := cloneRuntimeStepValue(step)
	if ec.ui != nil {
		ec.ui.PlannedStep(snapshot)
	}
	for _, hook := range ec.hooks {
		action, err := hook.PlannedStep(snapshot)
		if err != nil {
			return HookActionHalt, err
		}
		if action == HookActionHalt {
			return HookActionHalt, nil
		}
	}
	return HookActionContinue, nil
}

func (ec *BuiltinEvalContext) EmitExecutingStep(step *runbookruntime.Step) (HookAction, error) {
	if step == nil {
		return HookActionContinue, nil
	}
	ec.emitLock.Lock()
	defer ec.emitLock.Unlock()
	snapshot := cloneRuntimeStepValue(step)
	if ec.ui != nil {
		ec.ui.ExecutingStep(snapshot)
	}
	for _, hook := range ec.hooks {
		action, err := hook.ExecutingStep(snapshot)
		if err != nil {
			return HookActionHalt, err
		}
		if action == HookActionHalt {
			return HookActionHalt, nil
		}
	}
	return HookActionContinue, nil
}

func (ec *BuiltinEvalContext) EmitExecutedStep(step *runbookruntime.Step) (HookAction, error) {
	if step == nil {
		return HookActionContinue, nil
	}
	ec.emitLock.Lock()
	defer ec.emitLock.Unlock()
	snapshot := cloneRuntimeStepValue(step)
	if ec.ui != nil {
		ec.ui.ExecutedStep(snapshot)
	}
	for _, hook := range ec.hooks {
		action, err := hook.ExecutedStep(snapshot)
		if err != nil {
			return HookActionHalt, err
		}
		if action == HookActionHalt {
			return HookActionHalt, nil
		}
	}
	return HookActionContinue, nil
}

func (ec *BuiltinEvalContext) EmitActionEvent(event ActionExecEvent) (HookAction, error) {
	ec.emitLock.Lock()
	defer ec.emitLock.Unlock()
	if ec.ui != nil {
		ec.ui.ActionEvent(event)
	}
	for _, hook := range ec.hooks {
		action, err := hook.ActionEvent(event)
		if err != nil {
			return HookActionHalt, err
		}
		if action == HookActionHalt {
			return HookActionHalt, nil
		}
	}
	return HookActionContinue, nil
}

func (ec *BuiltinEvalContext) EmitStepPlanInfo(info StepPlanInfo) (HookAction, error) {
	ec.emitLock.Lock()
	defer ec.emitLock.Unlock()
	ec.planInfo = append(ec.planInfo, info)
	if ec.ui != nil {
		ec.ui.PlannedStepInfo(info)
	}
	for _, hook := range ec.hooks {
		action, err := hook.PlannedStepInfo(info)
		if err != nil {
			return HookActionHalt, err
		}
		if action == HookActionHalt {
			return HookActionHalt, nil
		}
	}
	return HookActionContinue, nil
}

func (ec *BuiltinEvalContext) PlanInfo() []StepPlanInfo {
	ec.emitLock.Lock()
	defer ec.emitLock.Unlock()
	ret := make([]StepPlanInfo, len(ec.planInfo))
	copy(ret, ec.planInfo)
	return ret
}

type stepEvalState struct {
	runtime *runbookruntime.Step

	locals  map[string]cty.Value
	data    map[string]cty.Value
	lists   map[string]cty.Value
	actions map[string]*actionEvalState
	outputs map[string]cty.Value
	waits   map[string]*waitEvalState
}

type actionEvalState struct {
	planned       bool
	invoked       bool
	plannedConfig cty.Value
}

type waitEvalState struct {
	Satisfied bool
	TimedOut  bool
	Attempts  int
	Elapsed   time.Duration
}

func (ec *BuiltinEvalContext) Config() *runbookconfigs.RunbookConfig {
	return ec.config
}

func (ec *BuiltinEvalContext) WorkspaceConfig() *configs.Config {
	if ec.config == nil {
		return nil
	}
	return ec.config.WorkspaceConfig
}

func (ec *BuiltinEvalContext) WorkspaceState() *states.State {
	return ec.workspaceState
}

func (ec *BuiltinEvalContext) SetVariable(name string, value *terraform.InputValue) {
	ec.variablesLock.Lock()
	defer ec.variablesLock.Unlock()

	ec.variables[name] = value
}

func (ec *BuiltinEvalContext) GetVariable(name string) (*terraform.InputValue, bool) {
	ec.variablesLock.RLock()
	defer ec.variablesLock.RUnlock()

	value, ok := ec.variables[name]
	return value, ok
}

func (ec *BuiltinEvalContext) ProviderInput(terraformaddrs.AbsProviderConfig) map[string]cty.Value {
	return nil
}

// SetProvider registers a provider instance keyed by its type with no alias.
// This is the common case when a runbook has a single configuration per provider.
func (ec *BuiltinEvalContext) SetProvider(providerType terraformaddrs.Provider, provider providers.Interface) {
	ec.SetProviderForConfig(terraformaddrs.AbsProviderConfig{
		Module:   terraformaddrs.RootModule,
		Provider: providerType,
	}, provider)
}

// SetProviderForConfig registers a provider instance for a specific provider
// configuration address, supporting aliased providers.
func (ec *BuiltinEvalContext) SetProviderForConfig(addr terraformaddrs.AbsProviderConfig, provider providers.Interface) {
	ec.providersLock.Lock()
	defer ec.providersLock.Unlock()

	ec.providers[addr.String()] = &providerEntry{instance: provider}
}

// Provider looks up a provider by type, returning the default (no-alias)
// configuration. For aliased providers, use ProviderForConfig.
func (ec *BuiltinEvalContext) Provider(providerType terraformaddrs.Provider) (providers.Interface, bool) {
	return ec.ProviderForConfig(terraformaddrs.AbsProviderConfig{
		Module:   terraformaddrs.RootModule,
		Provider: providerType,
	})
}

// ProviderForConfig looks up a provider by its full configuration address,
// including alias. If the exact address is not found and the requested alias
// is empty, it falls back to searching for any configuration of that provider
// type (backward compatibility for callers that don't track aliases).
func (ec *BuiltinEvalContext) ProviderForConfig(addr terraformaddrs.AbsProviderConfig) (providers.Interface, bool) {
	ec.providersLock.RLock()
	defer ec.providersLock.RUnlock()

	if entry, ok := ec.providerEntryForConfigLocked(addr); ok {
		return entry.instance, true
	}
	return nil, false
}

// providerEntryForConfigLocked resolves the pooled provider entry for a config
// address. Callers must hold providersLock (read or write). It performs an
// exact-address match first, then, if no alias was requested, falls back to any
// configuration of the same provider type (backward compatibility for callers
// that don't track aliases).
func (ec *BuiltinEvalContext) providerEntryForConfigLocked(addr terraformaddrs.AbsProviderConfig) (*providerEntry, bool) {
	if entry, ok := ec.providers[addr.String()]; ok && entry != nil {
		return entry, true
	}
	if addr.Alias == "" {
		for key, entry := range ec.providers {
			if entry != nil && strings.Contains(key, addr.Provider.String()) {
				return entry, true
			}
		}
	}
	return nil, false
}

// SetProviderFactories stores provider factories for creating fresh instances
// during parallel execution.
func (ec *BuiltinEvalContext) SetProviderFactories(factories map[terraformaddrs.Provider]providers.Factory) {
	ec.providerFactories = factories
}

// providerEntry holds a single pooled provider instance together with a guard
// that ensures the instance is ConfigureProvider'd exactly once, no matter how
// many parallel graph-walk goroutines request it. This is what makes a single
// shared instance safe to call concurrently: configuration happens once on
// first use, and every subsequent caller receives the already-configured
// instance with no re-Configure (fixing hc-terraform-wdc.3).
type providerEntry struct {
	instance    providers.Interface
	configOnce  sync.Once
	configDiags tfdiags.Diagnostics
}

// ConfiguredProviderForConfig returns the shared, pooled provider instance for
// the given configuration address, invoking the supplied configure function at
// most once across all goroutines to configure that instance. Subsequent
// callers receive the already-configured instance and the diagnostics produced
// by the single configure call.
//
// The configure function is run without holding providersLock so that the
// (potentially blocking) gRPC ConfigureProvider call does not serialize
// unrelated provider lookups. If no instance is registered for the address and
// none can be created from a registered factory, a missing-provider diagnostic
// is returned and configure is not called.
func (ec *BuiltinEvalContext) ConfiguredProviderForConfig(addr terraformaddrs.AbsProviderConfig, configure func(providers.Interface) tfdiags.Diagnostics) (providers.Interface, tfdiags.Diagnostics) {
	ec.providersLock.RLock()
	entry, ok := ec.providerEntryForConfigLocked(addr)
	ec.providersLock.RUnlock()
	if !ok {
		entry, ok = ec.ensureProviderEntry(addr)
	}
	if !ok || entry == nil || entry.instance == nil {
		return nil, missingProviderDiagnostic(addr.Provider, nil)
	}
	entry.configOnce.Do(func() {
		entry.configDiags = configure(entry.instance)
	})
	return entry.instance, entry.configDiags
}

// ensureProviderEntry lazily creates a pooled provider entry from a registered
// factory when one has not already been pre-registered by a producer. This is a
// safety net: both runbook plan producers normally pre-register one shared
// instance per provider type via SetProvider, so this path is only reached if a
// configuration address is requested that was not pre-registered.
func (ec *BuiltinEvalContext) ensureProviderEntry(addr terraformaddrs.AbsProviderConfig) (*providerEntry, bool) {
	ec.providersLock.Lock()
	defer ec.providersLock.Unlock()

	if entry, ok := ec.providerEntryForConfigLocked(addr); ok {
		return entry, true
	}
	if ec.providerFactories == nil {
		return nil, false
	}
	factory, ok := ec.providerFactories[addr.Provider]
	if !ok {
		return nil, false
	}
	instance, err := factory()
	if err != nil || instance == nil {
		return nil, false
	}
	entry := &providerEntry{instance: instance}
	ec.providers[addr.String()] = entry
	return entry, true
}

// closeProviders closes every pooled provider instance exactly once and clears
// the pool. It is the teardown counterpart to lazy creation/configuration and
// is called at the end of the execute walk (and for terminal plans) so that
// provider plugin subprocesses are reaped rather than leaked
// (fixing hc-terraform-wdc.2). Safe to call multiple times: the pool is emptied
// on the first call, so subsequent calls are no-ops.
func (ec *BuiltinEvalContext) closeProviders() tfdiags.Diagnostics {
	ec.providersLock.Lock()
	defer ec.providersLock.Unlock()

	var diags tfdiags.Diagnostics
	for key, entry := range ec.providers {
		if entry != nil && entry.instance != nil {
			if err := entry.instance.Close(); err != nil {
				diags = diags.Append(err)
			}
		}
		delete(ec.providers, key)
	}
	return diags
}

func (ec *BuiltinEvalContext) EnsureStep(name string, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step {
	return ec.ensureStepWithKey(name, terraformaddrs.NoKey, config, existing)
}

func (ec *BuiltinEvalContext) ensureStepWithKey(name string, instanceKey terraformaddrs.InstanceKey, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()
	return ec.ensureStepRuntimeLocked(name, instanceKey, config, existing)
}

func (ec *BuiltinEvalContext) ensurePlannedStepWithKey(name string, instanceKey terraformaddrs.InstanceKey, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()
	step := ec.ensureStepRuntimeLocked(name, instanceKey, config, existing)
	if step.Status == runbookruntime.StepStatusPending {
		step.Status = runbookruntime.StepStatusPlanned
	}
	return step
}

func (ec *BuiltinEvalContext) ensureRunningStepWithKey(name string, instanceKey terraformaddrs.InstanceKey, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()
	step := ec.ensureStepRuntimeLocked(name, instanceKey, config, existing)
	if step.Status == runbookruntime.StepStatusPending || step.Status == runbookruntime.StepStatusPlanned {
		step.Status = runbookruntime.StepStatusRunning
	}
	return step
}

func (ec *BuiltinEvalContext) ensureStepRuntimeLocked(name string, instanceKey terraformaddrs.InstanceKey, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step {
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

func (ec *BuiltinEvalContext) Step(name string) (*runbookruntime.Step, bool) {
	return ec.stepWithKey(name, terraformaddrs.NoKey)
}

func (ec *BuiltinEvalContext) stepWithKey(name string, instanceKey terraformaddrs.InstanceKey) (*runbookruntime.Step, bool) {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()

	state, ok := ec.steps[stepStateKey(name, instanceKey)]
	if !ok || state.runtime == nil {
		return nil, false
	}
	return cloneRuntimeStepValue(state.runtime), true
}

func (ec *BuiltinEvalContext) StepsInOrder() []*runbookruntime.Step {
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

func (ec *BuiltinEvalContext) SetStepStatus(name string, status runbookruntime.StepStatus, reason string) {
	ec.setStepStatusWithKey(name, terraformaddrs.NoKey, status, reason)
}

func (ec *BuiltinEvalContext) setStepStatusWithKey(name string, instanceKey terraformaddrs.InstanceKey, status runbookruntime.StepStatus, reason string) {
	var snapshot *runbookruntime.Step
	ec.stepsLock.Lock()

	key := stepStateKey(name, instanceKey)
	state := ec.ensureStepStateLocked(key)
	if state.runtime == nil {
		state.runtime = &runbookruntime.Step{Name: name, InstanceKey: instanceKey}
	}
	if state.runtime.Status == status && (reason == "" || state.runtime.SkipReason == reason) {
		ec.stepsLock.Unlock()
		return
	}
	state.runtime.Status = status
	if reason != "" {
		state.runtime.SkipReason = reason
	}
	snapshot = cloneRuntimeStepValue(state.runtime)
	ec.stepsLock.Unlock()
	ec.EmitExecutedStep(snapshot)
}

func (ec *BuiltinEvalContext) SetStepOutput(stepName, outputName string, value cty.Value) {
	ec.setStepOutputWithKey(stepName, terraformaddrs.NoKey, outputName, value)
}

func (ec *BuiltinEvalContext) setStepOutputWithKey(stepName string, instanceKey terraformaddrs.InstanceKey, outputName string, value cty.Value) {
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

func (ec *BuiltinEvalContext) StepOutput(stepName, outputName string) (cty.Value, bool) {
	return ec.stepOutputWithKey(stepName, terraformaddrs.NoKey, outputName)
}

func (ec *BuiltinEvalContext) stepOutputWithKey(stepName string, instanceKey terraformaddrs.InstanceKey, outputName string) (cty.Value, bool) {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()

	state, ok := ec.steps[stepStateKey(stepName, instanceKey)]
	if !ok {
		return cty.NilVal, false
	}
	value, ok := state.outputs[outputName]
	return value, ok
}

func (ec *BuiltinEvalContext) SetStepLocal(stepName, localName string, value cty.Value) {
	ec.setStepValueWithKey(stepName, terraformaddrs.NoKey, func(state *stepEvalState) {
		state.locals[localName] = value
	})
}

func (ec *BuiltinEvalContext) SetStepData(stepName string, addr terraformaddrs.Resource, value cty.Value) {
	ec.setStepValueWithKey(stepName, terraformaddrs.NoKey, func(state *stepEvalState) {
		state.data[addr.String()] = value
	})
}

func (ec *BuiltinEvalContext) SetStepList(stepName string, addr terraformaddrs.Resource, value cty.Value) {
	ec.setStepValueWithKey(stepName, terraformaddrs.NoKey, func(state *stepEvalState) {
		state.lists[addr.String()] = value
	})
}

func (ec *BuiltinEvalContext) MarkActionPlanned(stepName string, addr terraformaddrs.Action) {
	ec.setStepValueWithKey(stepName, terraformaddrs.NoKey, func(state *stepEvalState) {
		actionState, ok := state.actions[addr.String()]
		if !ok {
			actionState = &actionEvalState{}
			state.actions[addr.String()] = actionState
		}
		actionState.planned = true
	})
}

func (ec *BuiltinEvalContext) setActionPlannedWithKey(stepName string, instanceKey terraformaddrs.InstanceKey, actionKey string, config cty.Value) {
	ec.setStepValueWithKey(stepName, instanceKey, func(state *stepEvalState) {
		actionState, ok := state.actions[actionKey]
		if !ok {
			actionState = &actionEvalState{}
			state.actions[actionKey] = actionState
		}
		actionState.planned = true
		actionState.plannedConfig = config
	})
}

func (ec *BuiltinEvalContext) actionPlannedConfigWithKey(stepName string, instanceKey terraformaddrs.InstanceKey, actionKey string) (cty.Value, bool) {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()
	state, ok := ec.steps[stepStateKey(stepName, instanceKey)]
	if !ok {
		return cty.NilVal, false
	}
	actionState, ok := state.actions[actionKey]
	if !ok || actionState == nil || actionState.plannedConfig == cty.NilVal {
		return cty.NilVal, false
	}
	return actionState.plannedConfig, true
}

func (ec *BuiltinEvalContext) MarkActionInvoked(stepName string, addr terraformaddrs.Action) {
	ec.setStepValueWithKey(stepName, terraformaddrs.NoKey, func(state *stepEvalState) {
		actionState, ok := state.actions[addr.String()]
		if !ok {
			actionState = &actionEvalState{}
			state.actions[addr.String()] = actionState
		}
		actionState.invoked = true
	})
}

func (ec *BuiltinEvalContext) HasDependencyState(name string, statuses ...runbookruntime.StepStatus) bool {
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

func (ec *BuiltinEvalContext) stepHasStatusWithKey(name string, instanceKey terraformaddrs.InstanceKey, statuses ...runbookruntime.StepStatus) bool {
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

func (ec *BuiltinEvalContext) setStepValue(stepName string, apply func(state *stepEvalState)) {
	ec.setStepValueWithKey(stepName, terraformaddrs.NoKey, apply)
}

func (ec *BuiltinEvalContext) setStepValueWithKey(stepName string, instanceKey terraformaddrs.InstanceKey, apply func(state *stepEvalState)) {
	ec.stepsLock.Lock()
	defer ec.stepsLock.Unlock()

	key := stepStateKey(stepName, instanceKey)
	state := ec.ensureStepStateLocked(key)
	apply(state)
}

func (ec *BuiltinEvalContext) ensureStepStateLocked(key string) *stepEvalState {
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

func (ec *BuiltinEvalContext) StepLocal(stepName, localName string) (cty.Value, bool) {
	return ec.stepLocalWithKey(stepName, terraformaddrs.NoKey, localName)
}

func (ec *BuiltinEvalContext) stepLocalWithKey(stepName string, instanceKey terraformaddrs.InstanceKey, localName string) (cty.Value, bool) {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()

	state, ok := ec.steps[stepStateKey(stepName, instanceKey)]
	if !ok {
		return cty.NilVal, false
	}
	value, ok := state.locals[localName]
	return value, ok
}

func (ec *BuiltinEvalContext) StepData(stepName string, addr terraformaddrs.Resource) (cty.Value, bool) {
	return ec.stepDataWithKey(stepName, terraformaddrs.NoKey, addr)
}

func (ec *BuiltinEvalContext) stepDataWithKey(stepName string, instanceKey terraformaddrs.InstanceKey, addr terraformaddrs.Resource) (cty.Value, bool) {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()

	state, ok := ec.steps[stepStateKey(stepName, instanceKey)]
	if !ok {
		return cty.NilVal, false
	}
	value, ok := state.data[addr.String()]
	return value, ok
}

func (ec *BuiltinEvalContext) StepList(stepName string, addr terraformaddrs.Resource) (cty.Value, bool) {
	return ec.stepListWithKey(stepName, terraformaddrs.NoKey, addr)
}

func (ec *BuiltinEvalContext) stepListWithKey(stepName string, instanceKey terraformaddrs.InstanceKey, addr terraformaddrs.Resource) (cty.Value, bool) {
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()

	state, ok := ec.steps[stepStateKey(stepName, instanceKey)]
	if !ok {
		return cty.NilVal, false
	}
	value, ok := state.lists[addr.String()]
	return value, ok
}

func (ec *BuiltinEvalContext) EvaluateExpr(stepName string, expr hcl.Expression) (cty.Value, tfdiags.Diagnostics) {
	return ec.EvaluateExprForInstance(stepName, terraformaddrs.NoKey, nil, expr)
}

func (ec *BuiltinEvalContext) EvaluateExprForInstance(stepName string, instanceKey terraformaddrs.InstanceKey, repetitionData *terraform.InstanceKeyEvalData, expr hcl.Expression) (cty.Value, tfdiags.Diagnostics) {
	return ec.evaluateExprForInstance(stepName, instanceKey, repetitionData, nil, nil, expr)
}

// EvaluateCatchExpr evaluates an expression within a catch block's scope. The
// failed step and its diagnostics are threaded in per-evaluation (rather than
// read from a shared context slot) so that concurrent catch executions each see
// their own failed_step (hc-terraform-wdc.4).
func (ec *BuiltinEvalContext) EvaluateCatchExpr(failedStep *runbookruntime.Step, failDiags tfdiags.Diagnostics, expr hcl.Expression) (cty.Value, tfdiags.Diagnostics) {
	return ec.evaluateExprForInstance("", terraformaddrs.NoKey, nil, failedStep, failDiags, expr)
}

func (ec *BuiltinEvalContext) evaluateExprForInstance(stepName string, instanceKey terraformaddrs.InstanceKey, repetitionData *terraform.InstanceKeyEvalData, failedStep *runbookruntime.Step, failDiags tfdiags.Diagnostics, expr hcl.Expression) (cty.Value, tfdiags.Diagnostics) {
	if expr == nil {
		return cty.NilVal, nil
	}
	ec.emitWorkspaceReadPlanInfoForExpr(stepName, instanceKey, expr)
	if diags := validateReservedRunbookSymbolsInExpr(expr); diags.HasErrors() {
		return cty.DynamicVal, diags
	}
	if diags := ec.validateWorkspaceStateReferencesInExpr(expr); diags.HasErrors() {
		return cty.DynamicVal, diags
	}
	scope := &lang.Scope{BaseDir: ".", PureOnly: true}
	hclCtx := &hcl.EvalContext{
		Variables: ec.expressionVariablesForInstanceWithFailedStep(stepName, instanceKey, repetitionData, failedStep, failDiags),
		Functions: scope.Functions(),
	}
	value, hclDiags := expr.Value(hclCtx)
	return value, tfdiags.Diagnostics{}.Append(hclDiags)
}

func (ec *BuiltinEvalContext) expressionVariables(stepName string) map[string]cty.Value {
	return ec.expressionVariablesForInstance(stepName, terraformaddrs.NoKey, nil)
}

func (ec *BuiltinEvalContext) expressionVariablesForInstance(stepName string, instanceKey terraformaddrs.InstanceKey, repetitionData *terraform.InstanceKeyEvalData) map[string]cty.Value {
	return ec.expressionVariablesForInstanceWithFailedStep(stepName, instanceKey, repetitionData, nil, nil)
}

// expressionVariablesForInstanceWithFailedStep builds the expression scope for a
// step instance, optionally injecting the `failed_step` namespace from the
// supplied failed step and diagnostics. A nil failedStep omits the namespace
// (the normal, non-catch path). The failed step is passed per-evaluation rather
// than read from a shared field so concurrent catch executions don't interfere
// (hc-terraform-wdc.4).
func (ec *BuiltinEvalContext) expressionVariablesForInstanceWithFailedStep(stepName string, instanceKey terraformaddrs.InstanceKey, repetitionData *terraform.InstanceKeyEvalData, failedStep *runbookruntime.Step, failDiags tfdiags.Diagnostics) map[string]cty.Value {
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
		if len(state.waits) > 0 {
			variables["wait"] = waitVariables(state.waits)
		} else {
			variables["wait"] = waitVariablesFromConfig(ec.config, stepName)
		}
	} else {
		variables["local"] = cty.EmptyObjectVal
		variables["data"] = cty.EmptyObjectVal
		variables["list"] = cty.EmptyObjectVal
		variables["wait"] = waitVariablesFromConfig(ec.config, stepName)
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
	if ec.config != nil {
		for name, step := range ec.config.Steps {
			if step == nil {
				continue
			}
			if step.Count != nil || step.ForEach != nil {
				stepAttrs[name] = cty.EmptyObjectVal
				continue
			}
			outputs := map[string]cty.Value{}
			for _, output := range step.Outputs {
				if output == nil {
					continue
				}
				outputs[output.Name] = cty.DynamicVal
			}
			if len(outputs) == 0 {
				stepAttrs[name] = cty.EmptyObjectVal
				continue
			}
			stepAttrs[name] = cty.ObjectVal(outputs)
		}
	}
	for name, entries := range stepGroups {
		if len(entries) == 1 && entries[0].instance.InstanceKey == terraformaddrs.NoKey {
			// If step is failed/skipped, produce null outputs
			if entries[0].state.runtime != nil &&
				(entries[0].state.runtime.Status == runbookruntime.StepStatusFailed ||
					entries[0].state.runtime.Status == runbookruntime.StepStatusSkipped) {
				nullOutputs := map[string]cty.Value{}
				if ec.config != nil {
					if stepCfg, ok := ec.config.Steps[name]; ok {
						for _, output := range stepCfg.Outputs {
							if output != nil {
								nullOutputs[output.Name] = cty.NullVal(cty.DynamicPseudoType)
							}
						}
					}
				}
				if len(nullOutputs) == 0 {
					stepAttrs[name] = cty.EmptyObjectVal
				} else {
					stepAttrs[name] = cty.ObjectVal(nullOutputs)
				}
				continue
			}
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
	variables["workspace"] = ec.workspaceVariables()
	variables["action"] = ec.stepLocalActionVariables(stepName)

	// Catch-all support: inject failed_step and catch namespaces. failed_step is
	// taken from the per-evaluation argument (nil outside catch blocks).
	if failedStep != nil {
		variables["failed_step"] = buildFailedStepValue(failedStep, failDiags)
	}
	variables["catch"] = ec.catchVariables()

	return variables
}

// stepLocalActionVariables builds the top-level `action` namespace for a
// step's own expression scope (outputs, pre/postconditions). It exposes the
// step's declared actions as action.<type>.<name>. Action result attributes
// are not known until the action is invoked, so each action resolves to an
// unknown (dynamic) value; attribute access such as
// action.<type>.<name>.build_id therefore yields an unknown value and defers
// any condition that depends on it, matching the existing deferred-condition
// pattern.
func (ec *BuiltinEvalContext) stepLocalActionVariables(stepName string) cty.Value {
	if ec.config == nil {
		return cty.EmptyObjectVal
	}
	step, ok := ec.config.Steps[stepName]
	if !ok || step == nil {
		return cty.EmptyObjectVal
	}
	actions := map[string]map[string]cty.Value{}
	for _, action := range step.Actions {
		if action == nil {
			continue
		}
		if actions[action.Type] == nil {
			actions[action.Type] = map[string]cty.Value{}
		}
		actions[action.Type][action.Name] = cty.DynamicVal
	}
	if len(actions) == 0 {
		return cty.EmptyObjectVal
	}
	return nestedObjectValue(actions)
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

func (ec *BuiltinEvalContext) workspaceVariables() cty.Value {
	config := ec.WorkspaceConfig()
	if config == nil || config.Module == nil {
		return cty.EmptyObjectVal
	}
	return ec.workspaceModuleValue(config, config.Module, terraformaddrs.RootModuleInstance)
}

func (ec *BuiltinEvalContext) workspaceModuleValue(config *configs.Config, module *configs.Module, moduleAddr terraformaddrs.ModuleInstance) cty.Value {
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

	managedResources, dataResources := ec.workspaceResourceValues(moduleAddr)
	for typ, values := range managedResources {
		attrs[typ] = cty.ObjectVal(copyValueMap(values))
	}
	attrs["data"] = nestedObjectValue(dataResources)

	children := map[string]cty.Value{}
	for name, child := range config.Children {
		if child == nil || child.Module == nil {
			continue
		}
		children[name] = ec.workspaceChildModuleValue(name, child, moduleAddr)
	}
	attrs["module"] = cty.ObjectVal(copyValueMap(children))

	return cty.ObjectVal(attrs)
}

func (ec *BuiltinEvalContext) validateWorkspaceStateReferencesInExpr(expr hcl.Expression) tfdiags.Diagnostics {
	if expr == nil {
		return nil
	}
	var diags tfdiags.Diagnostics
	for _, traversal := range expr.Variables() {
		root, ok := traversal[0].(hcl.TraverseRoot)
		if !ok || root.Name != "workspace" {
			continue
		}
		ref, refDiags := runbookaddrs.ParseRef(traversal)
		diags = diags.Append(refDiags)
		if refDiags.HasErrors() || ref == nil {
			continue
		}
		resourceRef, ok := ref.Subject.(runbookaddrs.WorkspaceResource)
		if !ok {
			continue
		}
		if workspaceResourceConfig(ec.Config(), resourceRef) == nil {
			continue
		}
		if ec.workspaceResourceInState(resourceRef) {
			continue
		}
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Missing workspace state object",
			Detail:   fmt.Sprintf("The workspace reference %s is declared in workspace configuration but no instance is available in workspace state. Workspace resource and data references are state-backed.", workspaceRefString(ref)),
			Subject:  ref.SourceRange.ToHCL().Ptr(),
		})
	}
	return diags
}

func (ec *BuiltinEvalContext) workspaceResourceInState(ref runbookaddrs.WorkspaceResource) bool {
	state := ec.WorkspaceState()
	if state == nil {
		return false
	}
	moduleAddr := terraformaddrs.RootModuleInstance
	for _, call := range ref.Module.Calls {
		moduleAddr = moduleAddr.Child(call.Name, call.InstanceKey)
	}
	moduleState := state.Module(moduleAddr)
	if moduleState == nil {
		return false
	}
	resourceState := moduleState.Resource(ref.Resource)
	if resourceState == nil {
		return false
	}
	_, ok := workspaceResourceStateValue(resourceState)
	return ok
}

func (ec *BuiltinEvalContext) workspaceChildModuleValue(name string, child *configs.Config, parentAddr terraformaddrs.ModuleInstance) cty.Value {
	instances := ec.workspaceChildModuleInstances(parentAddr, name)
	if len(instances) == 0 {
		instances = []terraformaddrs.ModuleInstance{parentAddr.Child(name, terraformaddrs.NoKey)}
	}
	if len(instances) == 1 && instances[0][len(instances[0])-1].InstanceKey == terraformaddrs.NoKey {
		return ec.workspaceModuleValue(child, child.Module, instances[0])
	}
	vals := map[string]cty.Value{}
	for _, instance := range instances {
		call := instance[len(instance)-1]
		vals[instanceObjectKey(call.InstanceKey)] = ec.workspaceModuleValue(child, child.Module, instance)
	}
	return cty.ObjectVal(copyValueMap(vals))
}

func (ec *BuiltinEvalContext) workspaceChildModuleInstances(parentAddr terraformaddrs.ModuleInstance, name string) []terraformaddrs.ModuleInstance {
	state := ec.WorkspaceState()
	if state == nil {
		return nil
	}
	ret := make([]terraformaddrs.ModuleInstance, 0)
	for _, moduleState := range state.Modules {
		if moduleState == nil {
			continue
		}
		if len(moduleState.Addr) != len(parentAddr)+1 {
			continue
		}
		match := true
		for i := range parentAddr {
			if moduleState.Addr[i] != parentAddr[i] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		call := moduleState.Addr[len(moduleState.Addr)-1]
		if call.Name == name {
			ret = append(ret, moduleState.Addr)
		}
	}
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].String() < ret[j].String()
	})
	return ret
}

func (ec *BuiltinEvalContext) workspaceResourceValues(moduleAddr terraformaddrs.ModuleInstance) (map[string]map[string]cty.Value, map[string]map[string]cty.Value) {
	managed := map[string]map[string]cty.Value{}
	data := map[string]map[string]cty.Value{}
	state := ec.WorkspaceState()
	if state == nil {
		return managed, data
	}
	moduleState := state.Module(moduleAddr)
	if moduleState == nil {
		return managed, data
	}
	for _, resourceState := range moduleState.Resources {
		if resourceState == nil {
			continue
		}
		value, ok := workspaceResourceStateValue(resourceState)
		if !ok {
			continue
		}
		target := managed
		if resourceState.Addr.Resource.Mode == terraformaddrs.DataResourceMode {
			target = data
		}
		if target[resourceState.Addr.Resource.Type] == nil {
			target[resourceState.Addr.Resource.Type] = map[string]cty.Value{}
		}
		target[resourceState.Addr.Resource.Type][resourceState.Addr.Resource.Name] = value
	}
	return managed, data
}

func workspaceResourceStateValue(resourceState *states.Resource) (cty.Value, bool) {
	if resourceState == nil || len(resourceState.Instances) == 0 {
		return cty.NilVal, false
	}
	if len(resourceState.Instances) == 1 {
		if instance, ok := resourceState.Instances[terraformaddrs.NoKey]; ok && instance != nil && instance.Current != nil {
			value, ok := workspaceResourceInstanceValue(instance.Current)
			return value, ok
		}
	}
	instances := map[string]cty.Value{}
	for key, instance := range resourceState.Instances {
		if instance == nil || instance.Current == nil {
			continue
		}
		value, ok := workspaceResourceInstanceValue(instance.Current)
		if !ok {
			continue
		}
		instances[instanceObjectKey(key)] = value
	}
	if len(instances) == 0 {
		return cty.NilVal, false
	}
	return cty.ObjectVal(copyValueMap(instances)), true
}

func workspaceResourceInstanceValue(obj *states.ResourceInstanceObjectSrc) (cty.Value, bool) {
	if obj == nil {
		return cty.NilVal, false
	}
	if obj.AttrsJSON != nil {
		ty, err := ctyjson.ImpliedType(obj.AttrsJSON)
		if err == nil {
			value, err := ctyjson.Unmarshal(obj.AttrsJSON, ty)
			if err == nil {
				return value, true
			}
		}
	}
	if obj.AttrsFlat != nil {
		flat := make(map[string]cty.Value, len(obj.AttrsFlat))
		for k, v := range obj.AttrsFlat {
			flat[k] = cty.StringVal(v)
		}
		return cty.ObjectVal(flat), true
	}
	return cty.NilVal, false
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
		resource, ok := ref.Subject.(terraformaddrs.Resource)
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

func (ec *BuiltinEvalContext) EvaluateBlock(body hcl.Body, schema *configschema.Block) (cty.Value, hcl.Body, tfdiags.Diagnostics) {
	return ec.EvaluateBlockForInstance("", terraformaddrs.NoKey, nil, body, schema)
}

// EvaluateCatchBlock decodes an HCL body within a catch block's scope, threading
// the failed step in per-evaluation so concurrent catch executions each see
// their own failed_step (hc-terraform-wdc.4).
func (ec *BuiltinEvalContext) EvaluateCatchBlock(failedStep *runbookruntime.Step, failDiags tfdiags.Diagnostics, body hcl.Body, schema *configschema.Block) (cty.Value, hcl.Body, tfdiags.Diagnostics) {
	return ec.evaluateBlockForInstance("", terraformaddrs.NoKey, nil, failedStep, failDiags, body, schema)
}

func waitVariables(waits map[string]*waitEvalState) cty.Value {
	if len(waits) == 0 {
		return cty.EmptyObjectVal
	}
	attrs := make(map[string]cty.Value, len(waits))
	for name, ws := range waits {
		attrs[name] = cty.ObjectVal(map[string]cty.Value{
			"satisfied": cty.BoolVal(ws.Satisfied),
			"timed_out": cty.BoolVal(ws.TimedOut),
			"attempts":  cty.NumberIntVal(int64(ws.Attempts)),
			"elapsed":   cty.StringVal(ws.Elapsed.String()),
		})
	}
	return cty.ObjectVal(attrs)
}

// waitVariablesFromConfig returns unknown-valued wait attributes for all waits
// declared in the step config. Used at plan time before waits have executed.
func waitVariablesFromConfig(config *runbookconfigs.RunbookConfig, stepName string) cty.Value {
	if config == nil {
		return cty.EmptyObjectVal
	}
	step, ok := config.Steps[stepName]
	if !ok || step == nil {
		return cty.EmptyObjectVal
	}
	attrs := map[string]cty.Value{}
	for _, exec := range step.Executions {
		for _, op := range exec.Operations {
			if op.Type != runbookconfigs.ExecuteOpWait || op.Wait == nil {
				continue
			}
			attrs[op.Wait.Name] = cty.ObjectVal(map[string]cty.Value{
				"satisfied": cty.UnknownVal(cty.Bool),
				"timed_out": cty.UnknownVal(cty.Bool),
				"attempts":  cty.UnknownVal(cty.Number),
				"elapsed":   cty.UnknownVal(cty.String),
			})
		}
	}
	if len(attrs) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(attrs)
}

func (ec *BuiltinEvalContext) EvaluateBlockForInstance(stepName string, instanceKey terraformaddrs.InstanceKey, repetitionData *terraform.InstanceKeyEvalData, body hcl.Body, schema *configschema.Block) (cty.Value, hcl.Body, tfdiags.Diagnostics) {
	return ec.evaluateBlockForInstance(stepName, instanceKey, repetitionData, nil, nil, body, schema)
}

func (ec *BuiltinEvalContext) evaluateBlockForInstance(stepName string, instanceKey terraformaddrs.InstanceKey, repetitionData *terraform.InstanceKeyEvalData, failedStep *runbookruntime.Step, failDiags tfdiags.Diagnostics, body hcl.Body, schema *configschema.Block) (cty.Value, hcl.Body, tfdiags.Diagnostics) {
	if schema == nil {
		return cty.EmptyObjectVal, body, nil
	}
	if body == nil {
		return schema.EmptyValue(), nil, nil
	}
	ec.emitWorkspaceReadPlanInfoForBody(stepName, instanceKey, body)
	if diags := validateReservedRunbookSymbolsInBody(body); diags.HasErrors() {
		return cty.DynamicVal, body, diags
	}

	funcs := (&lang.Scope{BaseDir: ".", PureOnly: true, ForProvider: true}).Functions()
	hclCtx := &hcl.EvalContext{
		Variables: ec.expressionVariablesForInstanceWithFailedStep(stepName, instanceKey, repetitionData, failedStep, failDiags),
		Functions: funcs,
	}
	var diags tfdiags.Diagnostics
	expandedBody := dynblock.Expand(body, hclCtx)
	fixedBody := blocktoattr.FixUpBlockAttrs(expandedBody, schema)
	val, evalDiags := hcldec.Decode(fixedBody, schema.DecoderSpec(), hclCtx)
	diags = diags.Append(evalDiags)
	return val, fixedBody, diags
}

func (ec *BuiltinEvalContext) emitWorkspaceReadPlanInfoForExpr(stepName string, instanceKey terraformaddrs.InstanceKey, expr hcl.Expression) {
	if expr == nil || stepName == "" {
		return
	}
	ec.emitWorkspaceReadPlanInfo(stepName, instanceKey, expr.Variables())
}

func (ec *BuiltinEvalContext) emitWorkspaceReadPlanInfoForBody(stepName string, instanceKey terraformaddrs.InstanceKey, body hcl.Body) {
	if body == nil || stepName == "" {
		return
	}
	attrs, _ := body.JustAttributes()
	traversals := make([]hcl.Traversal, 0)
	for _, attr := range attrs {
		traversals = append(traversals, attr.Expr.Variables()...)
	}
	content, _, _ := body.PartialContent(&hcl.BodySchema{})
	for _, block := range content.Blocks {
		ec.emitWorkspaceReadPlanInfoForBody(stepName, instanceKey, block.Body)
	}
	ec.emitWorkspaceReadPlanInfo(stepName, instanceKey, traversals)
}

func (ec *BuiltinEvalContext) emitWorkspaceReadPlanInfo(stepName string, instanceKey terraformaddrs.InstanceKey, traversals []hcl.Traversal) {
	for _, traversal := range traversals {
		if len(traversal) == 0 {
			continue
		}
		root, ok := traversal[0].(hcl.TraverseRoot)
		if !ok || root.Name != "workspace" {
			continue
		}
		ref, diags := runbookaddrs.ParseRef(traversal)
		if diags.HasErrors() || ref == nil {
			continue
		}
		resourceRef, ok := ref.Subject.(runbookaddrs.WorkspaceResource)
		if !ok {
			continue
		}
		resourceCfg := workspaceResourceConfig(ec.Config(), resourceRef)
		if resourceCfg == nil {
			continue
		}
		attrs := workspaceRemainingTraversalAttrs(ref.Remaining)
		key := fmt.Sprintf("%s|%s|%s", stepName, resourceRef.String(), strings.Join(attrs, "."))
		ec.workspaceReadLock.Lock()
		if _, exists := ec.workspaceReads[key]; exists {
			ec.workspaceReadLock.Unlock()
			continue
		}
		ec.workspaceReads[key] = struct{}{}
		ec.workspaceReadLock.Unlock()
		details := map[string]cty.Value{
			"kind":       cty.StringVal(workspaceResourceKind(resourceRef)),
			"provider":   cty.StringVal(providerTypeForResource(ec.Config(), resourceCfg).ForDisplay()),
			"source":     cty.StringVal("workspace_state"),
			"attributes": stringListValue(attrs),
		}
		ec.EmitStepPlanInfo(StepPlanInfo{
			StepName:  stepName,
			StepIndex: stepIndexForNameAndKey(ec, stepName, instanceKey),
			Type:      "workspace_read",
			Subject:   resourceRef.String(),
			Status:    runbookruntime.StepStatusPlanned,
			Details:   cty.ObjectVal(details),
		})
	}
}

func stepIndexForNameAndKey(ec *BuiltinEvalContext, stepName string, instanceKey terraformaddrs.InstanceKey) int {
	if ec == nil {
		return 0
	}
	ec.stepsLock.RLock()
	defer ec.stepsLock.RUnlock()
	state, ok := ec.steps[stepStateKey(stepName, instanceKey)]
	if !ok || state == nil || state.runtime == nil {
		return 0
	}
	return state.runtime.Index
}

func workspaceResourceKind(ref runbookaddrs.WorkspaceResource) string {
	if ref.Resource.Mode == terraformaddrs.DataResourceMode {
		return "data"
	}
	return "resource"
}

func workspaceRemainingTraversalAttrs(traversal hcl.Traversal) []string {
	attrs := make([]string, 0, len(traversal))
	for _, step := range traversal {
		attr, ok := step.(hcl.TraverseAttr)
		if !ok {
			continue
		}
		attrs = append(attrs, attr.Name)
	}
	return attrs
}

func stringListValue(values []string) cty.Value {
	if len(values) == 0 {
		return cty.ListValEmpty(cty.String)
	}
	ret := make([]cty.Value, 0, len(values))
	for _, value := range values {
		ret = append(ret, cty.StringVal(value))
	}
	return cty.ListVal(ret)
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

// --- Catch-all support ---

// SetCatchOutput stores an output value produced by a catch block.
func (ec *BuiltinEvalContext) SetCatchOutput(catchName, outputName string, value cty.Value) {
	ec.catchLock.Lock()
	defer ec.catchLock.Unlock()
	if ec.catchOutputs == nil {
		ec.catchOutputs = make(map[string]map[string]cty.Value)
	}
	if ec.catchOutputs[catchName] == nil {
		ec.catchOutputs[catchName] = make(map[string]cty.Value)
	}
	ec.catchOutputs[catchName][outputName] = value
}

// CatchOutput retrieves an output value from a catch block.
func (ec *BuiltinEvalContext) CatchOutput(catchName, outputName string) (cty.Value, bool) {
	ec.catchLock.RLock()
	defer ec.catchLock.RUnlock()
	if ec.catchOutputs == nil {
		return cty.NilVal, false
	}
	outputs, ok := ec.catchOutputs[catchName]
	if !ok {
		return cty.NilVal, false
	}
	val, ok := outputs[outputName]
	return val, ok
}

// buildFailedStepValue constructs the cty.ObjectVal for the failed_step
// variable from the given failed step and its diagnostics. It is a pure
// function of its arguments (no shared context state) so it is safe to call
// concurrently from independent catch executions (hc-terraform-wdc.4).
func buildFailedStepValue(step *runbookruntime.Step, diags tfdiags.Diagnostics) cty.Value {
	if step == nil {
		return cty.EmptyObjectVal
	}

	instanceKey := ""
	if step.InstanceKey != nil {
		instanceKey = step.InstanceKey.String()
	}

	// Build diagnostics list
	diagsList := make([]cty.Value, 0)
	var firstSummary, firstDetail string
	for _, diag := range diags {
		if diag.Severity() != tfdiags.Error {
			continue
		}
		desc := diag.Description()
		if firstSummary == "" {
			firstSummary = desc.Summary
			firstDetail = desc.Detail
		}
		sourceFile := ""
		sourceLine := 0
		if src := diag.Source(); src.Subject != nil {
			sourceFile = src.Subject.Filename
			sourceLine = src.Subject.Start.Line
		}
		diagsList = append(diagsList, cty.ObjectVal(map[string]cty.Value{
			"severity":    cty.StringVal("error"),
			"summary":     cty.StringVal(desc.Summary),
			"detail":      cty.StringVal(desc.Detail),
			"source_file": cty.StringVal(sourceFile),
			"source_line": cty.NumberIntVal(int64(sourceLine)),
		}))
	}

	var diagnosticsVal cty.Value
	if len(diagsList) > 0 {
		diagnosticsVal = cty.ListVal(diagsList)
	} else {
		diagnosticsVal = cty.ListValEmpty(cty.Object(map[string]cty.Type{
			"severity":    cty.String,
			"summary":     cty.String,
			"detail":      cty.String,
			"source_file": cty.String,
			"source_line": cty.Number,
		}))
	}

	outputs := step.Outputs
	if outputs == cty.NilVal {
		outputs = cty.EmptyObjectVal
	}

	return cty.ObjectVal(map[string]cty.Value{
		"name":          cty.StringVal(step.Name),
		"instance_key":  cty.StringVal(instanceKey),
		"index":         cty.NumberIntVal(int64(step.Index)),
		"outputs":       outputs,
		"error_message": cty.StringVal(firstDetail),
		"error_summary": cty.StringVal(firstSummary),
		"diagnostics":   diagnosticsVal,
	})
}

// catchVariables builds the catch.* namespace for expression evaluation.
func (ec *BuiltinEvalContext) catchVariables() cty.Value {
	ec.catchLock.RLock()
	defer ec.catchLock.RUnlock()

	if len(ec.catchOutputs) == 0 && ec.config != nil && len(ec.config.Catches) == 0 {
		return cty.EmptyObjectVal
	}

	catchAttrs := map[string]cty.Value{}

	// Pre-fill with nulls for declared catches
	if ec.config != nil {
		for name, catch := range ec.config.Catches {
			outputs := map[string]cty.Value{}
			for _, output := range catch.Outputs {
				if output != nil {
					outputs[output.Name] = cty.NullVal(cty.DynamicPseudoType)
				}
			}
			if len(outputs) == 0 {
				catchAttrs[name] = cty.EmptyObjectVal
			} else {
				catchAttrs[name] = cty.ObjectVal(outputs)
			}
		}
	}

	// Override with actual values
	for catchName, outputs := range ec.catchOutputs {
		if len(outputs) == 0 {
			continue
		}
		catchAttrs[catchName] = cty.ObjectVal(copyValueMap(outputs))
	}

	if len(catchAttrs) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(catchAttrs)
}
