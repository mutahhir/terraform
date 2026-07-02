package runbookgraph

import (
	"errors"
	"testing"

	"github.com/hashicorp/hcl/v2"
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/providers"
	testing_provider "github.com/hashicorp/terraform/internal/providers/testing"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runtime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

func TestBuildPlanCreatesOrderedStepInstances(t *testing.T) {
	plan, diags := BuildPlan(&runbookconfigs.RunbookConfig{
		Variables: map[string]*configs.Variable{
			"input": {Name: "input"},
		},
		Steps: map[string]*runbookconfigs.Step{
			"producer": {
				Name:    "producer",
				Outputs: []*configs.Output{{Name: "result", Expr: mustParseExpression(t, `var.input`)}},
			},
			"consumer": {
				Name:    "consumer",
				Outputs: []*configs.Output{{Name: "final", Expr: mustParseExpression(t, `step.producer.result`)}},
			},
		},
	}, nil)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if plan == nil {
		t.Fatal("expected plan")
	}
	if plan.Graph == nil {
		t.Fatal("expected backing graph")
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 planned steps, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Name != "producer" {
		t.Fatalf("expected producer first, got %s", plan.Steps[0].Name)
	}
	if plan.Steps[1].Name != "consumer" {
		t.Fatalf("expected consumer second, got %s", plan.Steps[1].Name)
	}
	if plan.Steps[0].Index != 0 || plan.Steps[1].Index != 0 {
		t.Fatal("expected singleton step instances to use index 0")
	}
	if plan.Steps[0].Config == nil || plan.Steps[1].Config == nil {
		t.Fatal("expected planned steps to retain config")
	}
}

func TestBuildPlanRetainsPlannedRepetitionData(t *testing.T) {
	plan, diags := BuildPlan(&runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"deploy": {
				Name:    "deploy",
				ForEach: mustParseExpression(t, `{ primary = { value = "a" }, secondary = { value = "b" } }`),
			},
		},
	}, nil)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 planned step instances, got %d", len(plan.Steps))
	}
	for _, step := range plan.Steps {
		if step.RepetitionData == nil {
			t.Fatalf("expected repetition data for step %s", step.Name)
		}
		if step.RepetitionData.EachKey == cty.NilVal || step.RepetitionData.EachValue == cty.NilVal {
			t.Fatalf("expected for_each repetition data for step %s", step.Name)
		}
	}
	if got := len(plan.StepsRuntime()); got != 2 {
		t.Fatalf("expected distinct runtime entries for repeated instances, got %d", got)
	}
}

func TestBuildPlanPreservesRuntimeOutputsWhenProvided(t *testing.T) {
	stepConfig := &runbookconfigs.Step{Name: "discover"}
	plan, diags := BuildPlan(&runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"discover": stepConfig,
		},
	}, nil)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 planned step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Outputs != cty.NilVal {
		t.Fatal("expected planned step outputs to default to nil when runtime outputs are absent")
	}

	graph := mustBuildGraph(t, &PlanBuilder{Config: &runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{"discover": stepConfig},
	}, StepsRuntime: map[string]*runtime.Step{
		"discover": {Name: "discover", Config: stepConfig, Outputs: cty.StringVal("ok")},
	}})
	evalCtx := NewEvalContext(EvalContextOpts{Config: &runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{"discover": stepConfig},
	}})
	runtimeDiags := walkGraph(graph, evalCtx, walkOperationPlan, defaultRunbookParallelism)
	if runtimeDiags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", runtimeDiags.Err())
	}
	withRuntime := evalCtx.StepsInOrder()
	if len(withRuntime) != 1 {
		t.Fatalf("expected 1 expanded runtime step, got %d", len(withRuntime))
	}
	if got := withRuntime[0].Outputs.AsString(); got != "ok" {
		t.Fatalf("expected runtime outputs to be preserved, got %q", got)
	}
}

func TestPlanOutputValuesReturnsTopLevelRunbookOutputs(t *testing.T) {
	plan, diags := BuildPlan(&runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"producer": {
				Name:    "producer",
				Outputs: []*configs.Output{{Name: "result", Expr: mustParseExpression(t, `"srv-123"`)}},
			},
		},
		Outputs: map[string]*configs.Output{
			"summary": {Name: "summary", Expr: mustParseExpression(t, `step.producer.result`)},
		},
	}, nil)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if executeDiags := ExecutePlan(plan, nil); executeDiags.HasErrors() {
		t.Fatalf("unexpected execute diagnostics: %s", executeDiags.Err())
	}

	outputs, outputDiags := plan.OutputValues()
	if outputDiags.HasErrors() {
		t.Fatalf("unexpected output diagnostics: %s", outputDiags.Err())
	}
	if len(outputs) != 1 {
		t.Fatalf("expected 1 top-level output, got %d", len(outputs))
	}
	if got := outputs["summary"].AsString(); got != "srv-123" {
		t.Fatalf("expected summary output, got %q", got)
	}
}

func TestPlanOutputValuesRetainsErroredOutputAsUnknown(t *testing.T) {
	// An output whose expression errors at eval time must still appear in the
	// returned map (as unknown) rather than vanishing, so `runbook execute`
	// renders the output name alongside the surfaced error (hc-terraform-uns).
	plan, diags := BuildPlan(&runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"producer": {
				Name:    "producer",
				Outputs: []*configs.Output{{Name: "result", Expr: mustParseExpression(t, `"ok"`)}},
			},
		},
		Outputs: map[string]*configs.Output{
			"good": {Name: "good", Expr: mustParseExpression(t, `step.producer.result`)},
			"bad":  {Name: "bad", Expr: mustParseExpression(t, `tonumber("not-a-number")`)},
		},
	}, nil)
	if diags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", diags.Err())
	}
	if executeDiags := ExecutePlan(plan, nil); executeDiags.HasErrors() {
		t.Fatalf("unexpected execute diagnostics: %s", executeDiags.Err())
	}

	outputs, outputDiags := plan.OutputValues()
	if !outputDiags.HasErrors() {
		t.Fatal("expected an error diagnostic for the failing output")
	}
	// Both keys must be present — the errored one is no longer dropped.
	if _, ok := outputs["good"]; !ok {
		t.Fatal("expected 'good' output to be present")
	}
	bad, ok := outputs["bad"]
	if !ok {
		t.Fatal("expected errored 'bad' output to be retained, not dropped (hc-terraform-uns)")
	}
	if bad.IsKnown() {
		t.Fatalf("expected errored output to be unknown placeholder, got %#v", bad)
	}
}

func TestBuildPlanValidatesProviderBackedStepDeclarations(t *testing.T) {
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Actions: map[string]providers.ActionSchema{
				"test_action": {ConfigSchema: &configschema.Block{}},
			},
			DataSources: map[string]providers.Schema{
				"test_data": {Body: &configschema.Block{}},
			},
			ListResourceTypes: map[string]providers.Schema{
				"test_list": {Body: &configschema.Block{}},
			},
		},
	}

	_, diags := BuildPlan(&runbookconfigs.RunbookConfig{
		ProviderRequirements: &configs.RequiredProviders{
			RequiredProviders: map[string]*configs.RequiredProvider{
				"test": {Type: terraformaddrs.NewDefaultProvider("test")},
			},
		},
		Steps: map[string]*runbookconfigs.Step{
			"discover": {
				Name:        "discover",
				Actions:     []*configs.Action{{Type: "test_action", Name: "run"}},
				DataSources: []*configs.Resource{{Mode: terraformaddrs.DataResourceMode, Type: "test_data", Name: "lookup"}},
				ListResources: []*configs.Resource{{
					Mode: terraformaddrs.ListResourceMode,
					Type: "test_list",
					Name: "query",
					List: &configs.ListResource{},
				}},
			},
		},
	}, &PlannerOpts{
		Providers: map[terraformaddrs.Provider]providers.Factory{
			terraformaddrs.NewDefaultProvider("test"): fixedPlannerProviderFactory(provider),
		},
	})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if !provider.ValidateActionConfigCalled {
		t.Fatal("expected action config validation during plan")
	}
	if !provider.ValidateDataResourceConfigCalled {
		t.Fatal("expected data source config validation during plan")
	}
	if !provider.ValidateListResourceConfigCalled {
		t.Fatal("expected list resource config validation during plan")
	}
}

func TestBuildPlanReturnsProviderDeclarationDiagnostics(t *testing.T) {
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{},
	}

	_, diags := BuildPlan(&runbookconfigs.RunbookConfig{
		ProviderRequirements: &configs.RequiredProviders{
			RequiredProviders: map[string]*configs.RequiredProvider{
				"test": {Type: terraformaddrs.NewDefaultProvider("test")},
			},
		},
		Steps: map[string]*runbookconfigs.Step{
			"discover": {
				Name:    "discover",
				Actions: []*configs.Action{{Type: "unknown_action", Name: "run"}},
			},
		},
	}, &PlannerOpts{
		Providers: map[terraformaddrs.Provider]providers.Factory{
			terraformaddrs.NewDefaultProvider("test"): fixedPlannerProviderFactory(provider),
		},
	})
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}

func TestBuildPlanUsesRequiredProviderSourceForStepActions(t *testing.T) {
	providerType := terraformaddrs.MustParseProviderSourceString("registry.terraform.io/austinvalle/bufo")
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Actions: map[string]providers.ActionSchema{
				"bufo_print": {ConfigSchema: &configschema.Block{}},
			},
		},
	}

	_, diags := BuildPlan(&runbookconfigs.RunbookConfig{
		ProviderRequirements: &configs.RequiredProviders{
			RequiredProviders: map[string]*configs.RequiredProvider{
				"bufo": {Type: providerType},
			},
		},
		Steps: map[string]*runbookconfigs.Step{
			"discover": {
				Name:    "discover",
				Actions: []*configs.Action{{Type: "bufo_print", Name: "summary"}},
			},
		},
	}, &PlannerOpts{
		Providers: map[terraformaddrs.Provider]providers.Factory{
			providerType: fixedPlannerProviderFactory(provider),
		},
	})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if !provider.ValidateActionConfigCalled {
		t.Fatal("expected action config validation during plan")
	}
}

func TestBuildPlanAllowsActionConfigToReferenceSameStepLocalAtPlanTime(t *testing.T) {
	providerType := terraformaddrs.NewDefaultProvider("test")
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {
					ConfigSchema: &configschema.Block{
						Attributes: map[string]*configschema.Attribute{
							"target": {Type: cty.String, Optional: true},
						},
					},
				},
			},
			DataSources: map[string]providers.Schema{
				"test_data": {Body: &configschema.Block{}},
			},
		},
		ReadDataSourceResponse: &providers.ReadDataSourceResponse{State: cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("srv-123")})},
	}

	_, diags := BuildPlan(&runbookconfigs.RunbookConfig{
		ProviderRequirements: &configs.RequiredProviders{
			RequiredProviders: map[string]*configs.RequiredProvider{
				"test": {Type: providerType},
			},
		},
		Steps: map[string]*runbookconfigs.Step{
			"discover": {
				Name:        "discover",
				DataSources: []*configs.Resource{{Mode: terraformaddrs.DataResourceMode, Type: "test_data", Name: "selected"}},
				Locals:      []*configs.Local{{Name: "selected_id", Expr: mustParseExpression(t, `data.test_data.selected.id`)}},
				Actions:     []*configs.Action{{Type: "test_action", Name: "notify", Config: mustParseBody(t, `target = local.selected_id`)}},
			},
		},
	}, &PlannerOpts{
		Providers: map[terraformaddrs.Provider]providers.Factory{
			providerType: fixedPlannerProviderFactory(provider),
		},
	})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if got := provider.PlanActionRequest.ProposedActionData.GetAttr("target").AsString(); got != "srv-123" {
		t.Fatalf("expected action config target srv-123, got %q", got)
	}
}

func TestBuildPlanAllowsActionConfigToReferenceEachValueAtPlanTime(t *testing.T) {
	providerType := terraformaddrs.NewDefaultProvider("test")
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {
					ConfigSchema: &configschema.Block{
						Attributes: map[string]*configschema.Attribute{
							"target": {Type: cty.String, Optional: true},
						},
					},
				},
			},
		},
	}

	_, diags := BuildPlan(&runbookconfigs.RunbookConfig{
		ProviderRequirements: &configs.RequiredProviders{
			RequiredProviders: map[string]*configs.RequiredProvider{
				"test": {Type: providerType},
			},
		},
		Steps: map[string]*runbookconfigs.Step{
			"invoke": {
				Name:    "invoke",
				ForEach: mustParseExpression(t, `{ primary = { name = "lambda-a" } }`),
				Actions: []*configs.Action{{Type: "test_action", Name: "notify", Config: mustParseBody(t, `target = each.value.name`)}},
			},
		},
	}, &PlannerOpts{
		Providers: map[terraformaddrs.Provider]providers.Factory{
			providerType: fixedPlannerProviderFactory(provider),
		},
	})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if got := provider.PlanActionRequest.ProposedActionData.GetAttr("target").AsString(); got != "lambda-a" {
		t.Fatalf("expected action config target lambda-a, got %q", got)
	}
}

func TestBuildPlanConfiguresProvidersFromRunbookProviderBlocks(t *testing.T) {
	providerType := terraformaddrs.NewDefaultProvider("test")
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{
				Attributes: map[string]*configschema.Attribute{
					"region": {Type: cty.String, Optional: true},
				},
			}},
			DataSources: map[string]providers.Schema{
				"test_data": {Body: &configschema.Block{}},
			},
		},
		ReadDataSourceResponse: &providers.ReadDataSourceResponse{State: cty.EmptyObjectVal},
	}

	_, diags := BuildPlan(&runbookconfigs.RunbookConfig{
		ProviderRequirements: &configs.RequiredProviders{
			RequiredProviders: map[string]*configs.RequiredProvider{
				"test": {Type: providerType},
			},
		},
		ProviderConfigs: map[string]*configs.Provider{
			"test": {
				Name:   "test",
				Config: mustParseBody(t, `region = var.region`),
			},
		},
		Variables: map[string]*configs.Variable{
			"region": {Name: "region", Default: cty.StringVal("us-east-1")},
		},
		Steps: map[string]*runbookconfigs.Step{
			"discover": {
				Name:        "discover",
				DataSources: []*configs.Resource{{Mode: terraformaddrs.DataResourceMode, Type: "test_data", Name: "current"}},
			},
		},
	}, &PlannerOpts{
		Providers: map[terraformaddrs.Provider]providers.Factory{
			providerType: fixedPlannerProviderFactory(provider),
		},
	})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if !provider.ConfigureProviderCalled {
		t.Fatal("expected provider to be configured during plan")
	}
	if got := provider.ConfigureProviderRequest.Config.GetAttr("region"); got != cty.StringVal("us-east-1") {
		t.Fatalf("wrong configured region %#v", got)
	}
}

func TestBuildPlanPassesFullListBlockValueToProvider(t *testing.T) {
	providerType := terraformaddrs.NewDefaultProvider("test")
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			ListResourceTypes: map[string]providers.Schema{
				"test_list": {
					Body: &configschema.Block{
						BlockTypes: map[string]*configschema.NestedBlock{
							"config": {
								Block: configschema.Block{
									Attributes: map[string]*configschema.Attribute{
										"region": {Type: cty.String, Optional: true},
									},
								},
								Nesting: configschema.NestingSingle,
							},
						},
					},
				},
			},
		},
		ListResourceFn: func(req providers.ListResourceRequest) providers.ListResourceResponse {
			if !req.Config.Type().HasAttribute("config") {
				return providers.ListResourceResponse{Diagnostics: tfdiags.Diagnostics{}.Append(assertFailed("expected list config wrapper"))}
			}
			if got := req.Config.GetAttr("config").GetAttr("region"); got != cty.StringVal("us-east-1") {
				return providers.ListResourceResponse{Diagnostics: tfdiags.Diagnostics{}.Append(assertFailed("wrong wrapped list config"))}
			}
			return providers.ListResourceResponse{Result: cty.ObjectVal(map[string]cty.Value{"data": cty.EmptyTupleVal})}
		},
	}

	_, diags := BuildPlan(&runbookconfigs.RunbookConfig{
		ProviderRequirements: &configs.RequiredProviders{
			RequiredProviders: map[string]*configs.RequiredProvider{
				"test": {Type: providerType},
			},
		},
		ProviderConfigs: map[string]*configs.Provider{
			"test": {Name: "test"},
		},
		Steps: map[string]*runbookconfigs.Step{
			"discover": {
				Name: "discover",
				ListResources: []*configs.Resource{{
					Mode:   terraformaddrs.ListResourceMode,
					Type:   "test_list",
					Name:   "query",
					Config: mustParseBody(t, `config { region = "us-east-1" }`),
					List:   &configs.ListResource{},
				}},
			},
		},
	}, &PlannerOpts{
		Providers: map[terraformaddrs.Provider]providers.Factory{
			providerType: fixedPlannerProviderFactory(provider),
		},
	})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
}

func TestNodeStepExecutionCanInvokeWorkspaceAction(t *testing.T) {
	providerType := terraformaddrs.NewDefaultProvider("test")
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {ConfigSchema: &configschema.Block{}},
			},
		},
		InvokeActionFn: func(providers.InvokeActionRequest) providers.InvokeActionResponse {
			return providers.InvokeActionResponse{}
		},
	}

	ctx := NewEvalContext(EvalContextOpts{Config: &runbookconfigs.RunbookConfig{
		ProviderRequirements: &configs.RequiredProviders{
			RequiredProviders: map[string]*configs.RequiredProvider{
				"test": {Type: providerType},
			},
		},
		ProviderConfigs: map[string]*configs.Provider{
			"test": {Name: "test"},
		},
		WorkspaceConfig: &configs.Config{Module: &configs.Module{
			Actions: map[string]*configs.Action{
				"action.test_action.workspace_ping": {Type: "test_action", Name: "workspace_ping", Provider: providerType},
			},
		}},
	}})
	ctx.SetProvider(providerType, provider)

	diags := (&NodeStepExecution{
		Step:      &NodeStepInstance{StepName: "deploy"},
		Index:     0,
		Execution: &runbookconfigs.Execution{InvokeAction: []hcl.Traversal{mustParseTraversal(t, `workspace.action.test_action.workspace_ping`)}},
	}).Execute(ctx, walkOperationExecute)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if !provider.InvokeActionCalled {
		t.Fatal("expected workspace action to be invoked during execute walk")
	}
}

func TestNodeStepExecutionCanInvokeWorkspaceModuleAction(t *testing.T) {
	providerType := terraformaddrs.NewDefaultProvider("test")
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {ConfigSchema: &configschema.Block{}},
			},
		},
		InvokeActionFn: func(providers.InvokeActionRequest) providers.InvokeActionResponse {
			return providers.InvokeActionResponse{}
		},
	}

	ctx := NewEvalContext(EvalContextOpts{Config: &runbookconfigs.RunbookConfig{
		ProviderRequirements: &configs.RequiredProviders{
			RequiredProviders: map[string]*configs.RequiredProvider{
				"test": {Type: providerType},
			},
		},
		ProviderConfigs: map[string]*configs.Provider{
			"test": {Name: "test"},
		},
		WorkspaceConfig: &configs.Config{
			Module: &configs.Module{},
			Children: map[string]*configs.Config{
				"child": {
					Module: &configs.Module{
						Actions: map[string]*configs.Action{
							"action.test_action.workspace_ping": {Type: "test_action", Name: "workspace_ping", Provider: providerType},
						},
					},
				},
			},
		},
	}})
	ctx.SetProvider(providerType, provider)

	diags := (&NodeStepExecution{
		Step:      &NodeStepInstance{StepName: "deploy"},
		Index:     0,
		Execution: &runbookconfigs.Execution{InvokeAction: []hcl.Traversal{mustParseTraversal(t, `workspace.module.child.action.test_action.workspace_ping`)}},
	}).Execute(ctx, walkOperationExecute)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if !provider.InvokeActionCalled {
		t.Fatal("expected module workspace action to be invoked during execute walk")
	}
}

func TestValidateRejectsUnknownWorkspaceResourceAttribute(t *testing.T) {
	providerType := terraformaddrs.NewDefaultProvider("aws")
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			ResourceTypes: map[string]providers.Schema{
				"aws_lambda_function": {
					Body: &configschema.Block{Attributes: map[string]*configschema.Attribute{
						"arn": {Type: cty.String, Computed: true},
					}},
				},
			},
		},
	}

	config := &runbookconfigs.RunbookConfig{
		WorkspaceConfig: &configs.Config{Module: &configs.Module{
			ManagedResources: map[string]*configs.Resource{"aws_lambda_function.main": {Mode: terraformaddrs.ManagedResourceMode, Type: "aws_lambda_function", Name: "main", Provider: providerType}},
		}},
		Steps: map[string]*runbookconfigs.Step{
			"summary": {Name: "summary", Outputs: []*configs.Output{{Name: "bad", Expr: mustParseExpression(t, `workspace.aws_lambda_function.main.missing`)}}},
		},
	}

	diags := validateStepDeclarations(config, NewEvalContext(EvalContextOpts{Config: config}), &ValidateOpts{Providers: map[terraformaddrs.Provider]providers.Factory{providerType: fixedPlannerProviderFactory(provider)}})
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for unknown workspace attribute")
	}
}

func TestValidateRejectsSensitiveWorkspaceResourceAttribute(t *testing.T) {
	providerType := terraformaddrs.NewDefaultProvider("aws")
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			ResourceTypes: map[string]providers.Schema{
				"aws_lambda_function": {
					Body: &configschema.Block{Attributes: map[string]*configschema.Attribute{
						"secret": {Type: cty.String, Computed: true, Sensitive: true},
					}},
				},
			},
		},
	}

	config := &runbookconfigs.RunbookConfig{
		WorkspaceConfig: &configs.Config{Module: &configs.Module{
			ManagedResources: map[string]*configs.Resource{"aws_lambda_function.main": {Mode: terraformaddrs.ManagedResourceMode, Type: "aws_lambda_function", Name: "main", Provider: providerType}},
		}},
		Steps: map[string]*runbookconfigs.Step{
			"summary": {Name: "summary", Outputs: []*configs.Output{{Name: "bad", Expr: mustParseExpression(t, `workspace.aws_lambda_function.main.secret`)}}},
		},
	}

	diags := validateStepDeclarations(config, NewEvalContext(EvalContextOpts{Config: config}), &ValidateOpts{Providers: map[terraformaddrs.Provider]providers.Factory{providerType: fixedPlannerProviderFactory(provider)}})
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for sensitive workspace attribute")
	}
}

func assertFailed(msg string) error {
	return errors.New(msg)
}

func mustBuildGraph(t *testing.T, builder *PlanBuilder) *terraform.Graph {
	t.Helper()
	graph, diags := builder.Build()
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	return graph
}

func fixedPlannerProviderFactory(provider providers.Interface) providers.Factory {
	return func() (providers.Interface, error) {
		return provider, nil
	}
}
