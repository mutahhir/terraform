package runbookgraph

import (
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/providers"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runtime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type PlannerOpts struct {
	InputValues terraform.InputValues
	Providers   map[terraformaddrs.Provider]providers.Factory
}

type Plan struct {
	Config *runbookconfigs.RunbookConfig
	Graph  *terraform.Graph
	Steps  []*runtime.Step
}

func BuildPlan(config *runbookconfigs.RunbookConfig, opts *PlannerOpts) (*Plan, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if config == nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Missing runbook config", "A runbook plan requires configuration."))
	}

	validateOpts := &ValidateOpts{}
	evalCtx := NewEvalContext(EvalContextOpts{Config: config})
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

	return &Plan{Config: config, Graph: graph, Steps: evalCtx.StepsInOrder()}, diags
}
