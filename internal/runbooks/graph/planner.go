package runbookgraph

import (
	"context"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/providers"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	runtime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/states"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

type PlannerOpts struct {
	StopCtx        context.Context
	InputValues    terraform.InputValues
	Providers      map[terraformaddrs.Provider]providers.Factory
	WorkspaceState *states.State
	UI             UI
	Hooks          []Hook
	// Parallelism bounds how many step/operation callbacks run concurrently
	// during the graph walk. Values <= 0 fall back to defaultRunbookParallelism
	// (10), matching Terraform core's default -parallelism.
	Parallelism int
}

type ExecuteOpts = PlannerOpts

type Plan struct {
	Config          *runbookconfigs.RunbookConfig
	Graph           *terraform.Graph
	Steps           []*runtime.Step
	PlanInfo        []StepPlanInfo
	ExecutionLayers []ExecutionLayer
	evalCtx         *BuiltinEvalContext
}

func (p *Plan) StepsRuntime() map[string]*runtime.Step {
	if p == nil || len(p.Steps) == 0 {
		return nil
	}
	ret := make(map[string]*runtime.Step, len(p.Steps))
	for _, step := range p.Steps {
		if step == nil {
			continue
		}
		key := runbookaddrs.StepInstance{StepName: step.Name, InstanceKey: step.InstanceKey}.String()
		ret[key] = step
	}
	return ret
}

func (p *Plan) OutputValues() (map[string]cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if p == nil || p.Config == nil || len(p.Config.Outputs) == 0 {
		return nil, diags
	}
	if p.evalCtx == nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Missing runbook plan context", "Runbook outputs require plan evaluation state."))
	}

	ret := make(map[string]cty.Value, len(p.Config.Outputs))
	for _, name := range sortNames(p.Config.Outputs) {
		output := p.Config.Outputs[name]
		if output == nil {
			continue
		}
		value, outputDiags := p.evalCtx.EvaluateExpr("", output.Expr)
		diags = diags.Append(outputDiags)
		if outputDiags.HasErrors() {
			// Keep the output visible with an unknown placeholder rather than
			// dropping the key entirely; the error is surfaced via diags
			// (hc-terraform-uns).
			ret[name] = cty.UnknownVal(cty.DynamicPseudoType)
			continue
		}
		ret[name] = value
	}
	return ret, diags
}

// newPlanEvalContext builds the BuiltinEvalContext shared by the runbook plan
// producers (BuildPlan from source and ImportSavedPlan from a saved file). It
// wires input variables and shared provider instances identically for both
// producers so they cannot silently diverge in how the evaluation context is
// constructed.
//
// Provider *factories* (used to mint fresh per-step provider instances during
// parallel execution) are intentionally not registered here. They are
// registered at execute time by ExecutePlan, which is the single point that
// needs them and the only place guaranteed to run for every producer.
func newPlanEvalContext(ctxOpts EvalContextOpts, variables terraform.InputValues, providerFactories map[terraformaddrs.Provider]providers.Factory) (*BuiltinEvalContext, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	evalCtx := NewEvalContext(ctxOpts)
	for name, value := range variables {
		evalCtx.SetVariable(name, value)
	}
	for providerType, factory := range providerFactories {
		provider, err := factory()
		if err != nil {
			diags = diags.Append(err)
			continue
		}
		evalCtx.SetProvider(providerType, provider)
	}
	return evalCtx, diags
}

func BuildPlan(config *runbookconfigs.RunbookConfig, opts *PlannerOpts) (*Plan, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if config == nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Missing runbook config", "A runbook plan requires configuration."))
	}

	validateOpts := &ValidateOpts{}
	var inputValues terraform.InputValues
	var providerFactories map[terraformaddrs.Provider]providers.Factory
	if opts != nil {
		inputValues = opts.InputValues
		providerFactories = opts.Providers
		validateOpts.Providers = opts.Providers
	}
	evalCtx, ctxDiags := newPlanEvalContext(EvalContextOpts{
		StopCtx:        optsStopCtx(opts),
		Config:         config,
		WorkspaceState: optsWorkspaceState(opts),
		UI:             optsUI(opts),
		Hooks:          optsHooks(opts),
	}, inputValues, providerFactories)
	diags = diags.Append(ctxDiags)
	diags = diags.Append(ValidateWithContext(config, evalCtx, validateOpts))
	diags = diags.Append(validateStepDeclarations(config, evalCtx, validateOpts))
	diags = diags.Append(validateStepReferences(config))
	if diags.HasErrors() {
		return nil, diags
	}

	builder := &PlanBuilder{Config: config}
	if opts != nil {
		builder.InputValues = opts.InputValues
	}
	graph, buildDiags := builder.Build()
	diags = diags.Append(buildDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	diags = diags.Append(walkGraph(graph, evalCtx, walkOperationPlan, optsParallelism(opts)))
	if diags.HasErrors() {
		return nil, diags
	}

	plan := &Plan{Config: config, Graph: graph, Steps: evalCtx.StepsInOrder(), PlanInfo: evalCtx.PlanInfo(), evalCtx: evalCtx}
	plan.ExecutionLayers = ComputeExecutionLayers(graph)
	// Annotate layers with skip status from evaluated steps
	annotateLayersWithStatus(plan)
	return plan, diags
}

func ExecutePlan(plan *Plan, opts *ExecuteOpts) tfdiags.Diagnostics {
	if plan == nil {
		return tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(tfdiags.Error, "Missing runbook plan", "Runbook execution requires a plan."))
	}
	evalCtx := plan.evalCtx
	if evalCtx == nil {
		return tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(tfdiags.Error, "Missing runbook plan context", "Runbook execution requires plan evaluation state."))
	}
	if opts != nil {
		evalCtx.ui = optsUI(opts)
		evalCtx.hooks = optsHooks(opts)
		// Register provider factories at execute time so per-step fresh
		// provider instantiation behaves identically regardless of which
		// producer (BuildPlan from source or ImportSavedPlan from a saved
		// file) built this plan. ExecutePlan is the sole registrar of
		// factories: producers intentionally do not set them (plan-time never
		// mints fresh instances). ExecuteOpts.Providers must therefore be
		// populated for per-step provider isolation; the nil check only avoids
		// overwriting with an empty map when a caller passes no providers.
		if opts.Providers != nil {
			evalCtx.SetProviderFactories(opts.Providers)
		}
	}
	evalCtx.stepsLock.Lock()
	for _, step := range plan.Steps {
		if step == nil {
			continue
		}
		state, ok := evalCtx.steps[stepStateKey(step.Name, step.InstanceKey)]
		if !ok || state == nil || state.runtime == nil {
			continue
		}
		switch state.runtime.Status {
		case runbookruntime.StepStatusSkipped, runbookruntime.StepStatusFailed:
			continue
		default:
			state.runtime.Status = runbookruntime.StepStatusPlanned
			state.runtime.SkipReason = ""
		}
	}
	evalCtx.stepsLock.Unlock()
	walkDiags := walkGraph(plan.Graph, evalCtx, walkOperationExecute, optsParallelism(opts))
	diags := walkDiags
	// Close pooled provider instances once execution finishes so plugin
	// subprocesses are reaped rather than leaked (hc-terraform-wdc.2). This is
	// the terminal point for the shared instances created/configured during the
	// plan and execute walks.
	diags = diags.Append(evalCtx.closeProviders())
	if walkDiags.HasErrors() {
		return diags
	}
	plan.Steps = evalCtx.StepsInOrder()
	return diags
}

// Close releases provider instances held by the plan's evaluation context. It
// is safe to call multiple times and is intended for plan-only flows
// (e.g. `runbook plan`) that build a plan but never execute it; the execute
// path closes providers itself at the end of ExecutePlan.
func (p *Plan) Close() tfdiags.Diagnostics {
	if p == nil || p.evalCtx == nil {
		return nil
	}
	return p.evalCtx.closeProviders()
}

// optsParallelism returns the configured walk parallelism, defaulting to
// defaultRunbookParallelism when unset or non-positive.
func optsParallelism(opts *PlannerOpts) int {
	if opts == nil || opts.Parallelism <= 0 {
		return defaultRunbookParallelism
	}
	return opts.Parallelism
}

func optsUI(opts *PlannerOpts) UI {
	if opts == nil {
		return nil
	}
	return opts.UI
}

func optsHooks(opts *PlannerOpts) []Hook {
	if opts == nil {
		return nil
	}
	return opts.Hooks
}

func optsWorkspaceState(opts *PlannerOpts) *states.State {
	if opts == nil {
		return nil
	}
	return opts.WorkspaceState
}

func optsStopCtx(opts *PlannerOpts) context.Context {
	if opts == nil || opts.StopCtx == nil {
		return nil
	}
	return opts.StopCtx
}
