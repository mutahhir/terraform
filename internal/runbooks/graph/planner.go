package runbookgraph

import (
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
	InputValues    terraform.InputValues
	Providers      map[terraformaddrs.Provider]providers.Factory
	WorkspaceState *states.State
	UI             UI
	Hooks          []Hook
}

type ExecuteOpts = PlannerOpts

type Plan struct {
	Config   *runbookconfigs.RunbookConfig
	Graph    *terraform.Graph
	Steps    []*runtime.Step
	PlanInfo []StepPlanInfo
	evalCtx  *EvalContext
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
			continue
		}
		ret[name] = value
	}
	return ret, diags
}

func BuildPlan(config *runbookconfigs.RunbookConfig, opts *PlannerOpts) (*Plan, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if config == nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Missing runbook config", "A runbook plan requires configuration."))
	}

	validateOpts := &ValidateOpts{}
	evalCtx := NewEvalContext(EvalContextOpts{Config: config, WorkspaceState: optsWorkspaceState(opts), UI: optsUI(opts), Hooks: optsHooks(opts)})
	if opts != nil {
		for name, value := range opts.InputValues {
			evalCtx.SetVariable(name, value)
		}
		for providerType, factory := range opts.Providers {
			provider, err := factory()
			if err != nil {
				diags = diags.Append(err)
				continue
			}
			evalCtx.SetProvider(providerType, provider)
		}
	}
	if opts != nil {
		validateOpts.Providers = opts.Providers
	}
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

	diags = diags.Append(walkGraph(graph, evalCtx, walkOperationPlan))
	if diags.HasErrors() {
		return nil, diags
	}

	return &Plan{Config: config, Graph: graph, Steps: evalCtx.StepsInOrder(), PlanInfo: evalCtx.PlanInfo(), evalCtx: evalCtx}, diags
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
	diags := walkGraph(plan.Graph, evalCtx, walkOperationExecute)
	if diags.HasErrors() {
		return diags
	}
	plan.Steps = evalCtx.StepsInOrder()
	return diags
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
