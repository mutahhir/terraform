// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/providers"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// EvalContext is the interface through which graph nodes interact with
// evaluation state during the runbook graph walk. It covers configuration
// access, expression evaluation, provider lookup, step state management,
// and event emission.
//
// Defining this as an interface (rather than passing the concrete *EvalContext)
// enables isolated testing of individual graph nodes with mock
// implementations, matching the pattern established by Terraform core's
// EvalContext interface in internal/terraform/eval_context.go.
type EvalContext interface {
	// --- Context ---

	// StopCtx returns a context that is cancelled when execution should stop.
	// Nodes should check this for graceful cancellation.
	StopCtx() context.Context

	// --- Configuration ---

	// Config returns the runbook configuration.
	Config() *runbookconfigs.RunbookConfig

	// WorkspaceConfig returns the parsed workspace (root module) configuration.
	WorkspaceConfig() *configs.Config

	// ProviderInput returns any input configuration for a provider address.
	ProviderInput(terraformaddrs.AbsProviderConfig) map[string]cty.Value

	// --- Expression Evaluation ---

	// EvaluateExpr evaluates an HCL expression within the given step's scope.
	EvaluateExpr(stepName string, expr hcl.Expression) (cty.Value, tfdiags.Diagnostics)

	// EvaluateExprForInstance evaluates an expression for a specific step instance,
	// with optional repetition data (count.index, each.key, each.value).
	EvaluateExprForInstance(stepName string, instanceKey terraformaddrs.InstanceKey, repetitionData *terraform.InstanceKeyEvalData, expr hcl.Expression) (cty.Value, tfdiags.Diagnostics)

	// EvaluateBlock decodes an HCL body against a schema within the default scope.
	EvaluateBlock(body hcl.Body, schema *configschema.Block) (cty.Value, hcl.Body, tfdiags.Diagnostics)

	// EvaluateBlockForInstance decodes an HCL body for a specific step instance.
	EvaluateBlockForInstance(stepName string, instanceKey terraformaddrs.InstanceKey, repetitionData *terraform.InstanceKeyEvalData, body hcl.Body, schema *configschema.Block) (cty.Value, hcl.Body, tfdiags.Diagnostics)

	// --- Provider Access ---

	// Provider returns a provider instance by type (default/no-alias configuration).
	Provider(providerType terraformaddrs.Provider) (providers.Interface, bool)

	// ProviderForConfig returns a provider instance by full configuration address (with alias).
	ProviderForConfig(addr terraformaddrs.AbsProviderConfig) (providers.Interface, bool)

	// NewProviderInstance creates a fresh provider instance from the factory.
	// Returns an error if no factory is registered for the given provider type.
	NewProviderInstance(providerType terraformaddrs.Provider) (providers.Interface, error)

	// --- Step State ---

	// EnsureStep initializes or retrieves runtime state for a step (no-key instance).
	EnsureStep(name string, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step

	// SetStepStatus sets the status and optional reason for a step (no-key instance).
	SetStepStatus(name string, status runbookruntime.StepStatus, reason string)

	// SetStepOutput stores an output value for a step (no-key instance).
	SetStepOutput(stepName, outputName string, value cty.Value)

	// StepOutput retrieves an output value for a step (no-key instance).
	StepOutput(stepName, outputName string) (cty.Value, bool)

	// Step retrieves the runtime state for a step (no-key instance).
	Step(name string) (*runbookruntime.Step, bool)

	// StepsInOrder returns all step runtime states in registration order.
	StepsInOrder() []*runbookruntime.Step

	// HasDependencyState checks if a step has any of the given statuses.
	HasDependencyState(name string, statuses ...runbookruntime.StepStatus) bool

	// --- Step State (instance-keyed, for count/for_each) ---

	ensureStepWithKey(name string, instanceKey terraformaddrs.InstanceKey, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step
	ensurePlannedStepWithKey(name string, instanceKey terraformaddrs.InstanceKey, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step
	ensureRunningStepWithKey(name string, instanceKey terraformaddrs.InstanceKey, config *runbookconfigs.Step, existing *runbookruntime.Step) *runbookruntime.Step
	setStepStatusWithKey(name string, instanceKey terraformaddrs.InstanceKey, status runbookruntime.StepStatus, reason string)
	setStepOutputWithKey(stepName string, instanceKey terraformaddrs.InstanceKey, outputName string, value cty.Value)
	stepHasStatusWithKey(name string, instanceKey terraformaddrs.InstanceKey, statuses ...runbookruntime.StepStatus) bool
	stepWithKey(name string, instanceKey terraformaddrs.InstanceKey) (*runbookruntime.Step, bool)
	setStepValueWithKey(stepName string, instanceKey terraformaddrs.InstanceKey, apply func(state *stepEvalState))
	setActionPlannedWithKey(stepName string, instanceKey terraformaddrs.InstanceKey, actionKey string, config cty.Value)
	actionPlannedConfigWithKey(stepName string, instanceKey terraformaddrs.InstanceKey, actionKey string) (cty.Value, bool)

	// --- Events ---

	// EmitPlannedStep notifies UI and hooks that a step has been planned.
	EmitPlannedStep(step *runbookruntime.Step) (HookAction, error)

	// EmitExecutingStep notifies that a step is now executing.
	EmitExecutingStep(step *runbookruntime.Step) (HookAction, error)

	// EmitExecutedStep notifies that a step has finished executing.
	EmitExecutedStep(step *runbookruntime.Step) (HookAction, error)

	// EmitStepPlanInfo emits plan-time metadata about a step operation.
	EmitStepPlanInfo(info StepPlanInfo) (HookAction, error)

	// EmitActionEvent emits an action execution event.
	EmitActionEvent(event ActionExecEvent) (HookAction, error)
}

// Compile-time check that the concrete EvalContext satisfies EvalContext.
var _ EvalContext = (*BuiltinEvalContext)(nil)
