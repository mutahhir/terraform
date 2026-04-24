// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/providers"
	testing_provider "github.com/hashicorp/terraform/internal/providers/testing"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runtime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/spf13/afero"
	"github.com/zclconf/go-cty/cty"
)

func TestBuildPlanConditionFailureDiagnosticsIncludeSourceRange(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

variable "enabled" {
  type    = bool
  default = false
}

step "deploy" {
  precondition {
    condition     = var.enabled
    error_message = "expected source-ranged failure"
  }
}
`)

	config := loadIntegrationRunbookConfig(t, fs)

	_, buildDiags := BuildPlan(config, &PlannerOpts{})
	if !buildDiags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
	if src := buildDiags[0].Source(); src.Subject == nil || src.Subject.Filename != "/runbook/main.tfrun.hcl" {
		t.Fatalf("expected source-ranged diagnostic, got: %#v", src)
	}
}

func TestPlanBuilderBuildIntegrationEmptyStepVariableAndOutput(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

variable "name" {
  type = string
}

output "summary" {
  value = var.name
}

step "deploy" {}
`)

	config := loadIntegrationRunbookConfig(t, fs)

	graph, buildDiags := (&PlanBuilder{Config: config}).Build()
	if buildDiags.HasErrors() {
		t.Fatalf("unexpected build diagnostics: %s", buildDiags.Err())
	}

	inputNode := rootVariableNode(graph, "name")
	stepNode := &NodeExpandStep{StepName: "deploy"}
	outputNode := &NodeOutputVariable{Output: &configs.Output{Name: "summary"}}

	if inputNode == nil {
		t.Fatal("expected terraform root input variable node")
	}
	if !graph.HasVertex(stepNode) {
		t.Fatal("expected empty step expansion node")
	}
	if !graph.HasVertex(outputNode) {
		t.Fatal("expected output node")
	}
	if !hasVertexNamed(graph, "root") {
		t.Fatal("expected root node")
	}

	deps := graph.DownEdges(vertexNamed(graph, "root"))
	if !deps.Include(inputNode) {
		t.Fatal("expected root to connect to input variable")
	}
	if !deps.Include(stepNode) {
		t.Fatal("expected root to connect to empty step expansion node")
	}
	if !deps.Include(outputNode) {
		t.Fatal("expected root to connect to output")
	}
	if graph.DownEdges(outputNode).Len() != 0 {
		t.Fatal("expected output to have no dependencies when none are declared")
	}
	if len(config.Steps["deploy"].Actions) != 0 {
		t.Fatal("expected empty step to have no actions")
	}
	if len(config.Steps["deploy"].Executions) != 0 {
		t.Fatal("expected empty step to have no executions")
	}
	if len(config.Steps["deploy"].Outputs) != 0 {
		t.Fatal("expected empty step to have no outputs")
	}
}

func TestBuildPlanIntegrationFullRunbookPlan(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

variable "name" {
  type    = string
  default = "mars"
}

provider "test" {}

step "discover" {
  data "test_data" "selected" {}

  list "test_list" "servers" {
    provider         = test
    include_resource = true
    limit            = 2
  }

  locals {
    selected_id = data.test_data.selected.id
  }

  action "test_action" "notify" {
    config {
      target = local.selected_id
    }
  }

  precondition {
    condition     = var.name == "mars"
    error_message = "expected mars"
  }

  output "result" {
    value = local.selected_id
  }
}

step "deploy" {
  precondition {
    condition     = step.discover.result == "srv-123"
    error_message = "discover must produce srv-123"
  }

  execute {
    invoke_action {
      action = action.test_action.notify
    }
  }

  output "summary" {
    value = step.discover.result
  }
}

output "summary" {
  value = step.deploy.summary
}
`)

	config := loadIntegrationRunbookConfig(t, fs)

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
				"test_data": {
					Body: &configschema.Block{},
				},
			},
			ListResourceTypes: map[string]providers.Schema{
				"test_list": {
					Body: &configschema.Block{},
				},
			},
		},
		ReadDataSourceResponse: &providers.ReadDataSourceResponse{
			State: cty.ObjectVal(map[string]cty.Value{
				"id": cty.StringVal("srv-123"),
			}),
		},
		ListResourceResponse: providers.ListResourceResponse{
			Result: cty.ObjectVal(map[string]cty.Value{
				"items": cty.TupleVal([]cty.Value{cty.StringVal("srv-123"), cty.StringVal("srv-456")}),
			}),
		},
	}

	plan, planDiags := BuildPlan(config, &PlannerOpts{
		Providers: map[addrs.Provider]providers.Factory{
			addrs.NewDefaultProvider("test"): fixedProviderFactory(provider),
		},
	})
	if planDiags.HasErrors() {
		if !provider.ValidateActionConfigCalled {
			t.Log("provider action validation was skipped as expected for runtime-dependent config")
		}
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}
	if plan == nil {
		t.Fatal("expected plan but got nil")
	}
	if plan.Graph == nil {
		t.Fatal("expected planned graph")
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 planned steps, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Name != "discover" {
		t.Fatalf("expected discover first, got %q", plan.Steps[0].Name)
	}
	if plan.Steps[1].Name != "deploy" {
		t.Fatalf("expected deploy second, got %q", plan.Steps[1].Name)
	}
	if plan.Steps[0].Status != runtime.StepStatusCompleted {
		t.Fatalf("expected discover to complete planning, got %q", plan.Steps[0].Status)
	}
	if plan.Steps[1].Status != runtime.StepStatusCompleted {
		t.Fatalf("expected deploy to complete planning, got %q", plan.Steps[1].Status)
	}
	if got := plan.Steps[0].Outputs.GetAttr("result").AsString(); got != "srv-123" {
		t.Fatalf("expected discover output result to be preserved, got %q", got)
	}
	if got := plan.Steps[1].Outputs.GetAttr("summary").AsString(); got != "srv-123" {
		t.Fatalf("expected deploy output summary to reference discover output, got %q", got)
	}
	if !provider.ConfigureProviderCalled {
		t.Fatal("expected provider to be configured during plan walk")
	}
	if !provider.ReadDataSourceCalled {
		t.Fatal("expected data source to be read during plan walk")
	}
	if !provider.ListResourceCalled {
		t.Fatal("expected list resource to be listed during plan walk")
	}
	if !provider.PlanActionCalled {
		t.Fatal("expected action to be planned during plan walk")
	}
	if provider.InvokeActionCalled {
		t.Fatal("expected execute blocks not to invoke actions during plan walk")
	}
}

func TestBuildPlanIntegrationProviderSchemaMismatch(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

step "discover" {
  action "test_action" "notify" {
    config {
      target = 1
    }
  }

  data "test_data" "selected" {
    target = true
  }

  list "test_list" "servers" {
    provider         = test
    include_resource = "yes"
    limit            = "many"

    config {
      target = []
    }
  }
}
`)

	config := loadIntegrationRunbookConfig(t, fs)

	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {
					ConfigSchema: &configschema.Block{
						Attributes: map[string]*configschema.Attribute{
							"target": {Type: cty.String, Required: true},
						},
					},
				},
			},
			DataSources: map[string]providers.Schema{
				"test_data": {
					Body: &configschema.Block{
						Attributes: map[string]*configschema.Attribute{
							"target": {Type: cty.String, Optional: true},
						},
					},
				},
			},
			ListResourceTypes: map[string]providers.Schema{
				"test_list": {
					Body: &configschema.Block{
						Attributes: map[string]*configschema.Attribute{
							"include_resource": {Type: cty.Bool, Optional: true},
							"limit":            {Type: cty.Number, Optional: true},
						},
						BlockTypes: map[string]*configschema.NestedBlock{
							"config": {
								Nesting: configschema.NestingSingle,
								Block: configschema.Block{
									Attributes: map[string]*configschema.Attribute{
										"target": {Type: cty.String, Optional: true},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	plan, planDiags := BuildPlan(config, &PlannerOpts{
		Providers: map[addrs.Provider]providers.Factory{
			addrs.NewDefaultProvider("test"): fixedProviderFactory(provider),
		},
	})
	if plan != nil {
		t.Fatal("expected plan to fail when runbook declarations violate provider schemas")
	}
	if !planDiags.HasErrors() {
		t.Fatal("expected schema diagnostics but got none")
	}
	got := planDiags.Err().Error()
	if !strings.Contains(got, "Incorrect attribute value type") && !strings.Contains(got, "Unsuitable value") {
		t.Fatalf("expected provider schema mismatch diagnostics, got: %s", got)
	}
	if !provider.ValidateActionConfigCalled {
		t.Fatal("expected action schema validation to run before failing")
	}
	if !provider.ValidateDataResourceConfigCalled {
		t.Fatal("expected data schema validation to run before failing")
	}
	if provider.ValidateListResourceConfigCalled {
		t.Fatal("expected list validation to stop before provider RPC when HCL evaluation already fails")
	}
}

func TestBuildPlanAllowsForEachFromEarlierListOutput(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

step "discover" {
  list "test_list" "servers" {
    provider = test
  }

  output "items" {
    value = list.test_list.servers.items
  }
}

step "deploy" {
  for_each = {
    for item in step.discover.items : item => {
      id = item
    }
  }

  output "summary" {
    value = each.value.id
  }
}
`)

	config := loadIntegrationRunbookConfig(t, fs)

	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			ListResourceTypes: map[string]providers.Schema{
				"test_list": {
					Body: &configschema.Block{},
				},
			},
		},
		ListResourceResponse: providers.ListResourceResponse{
			Result: cty.ObjectVal(map[string]cty.Value{
				"items": cty.TupleVal([]cty.Value{cty.StringVal("srv-123"), cty.StringVal("srv-456")}),
			}),
		},
	}

	plan, planDiags := BuildPlan(config, &PlannerOpts{
		Providers: map[addrs.Provider]providers.Factory{
			addrs.NewDefaultProvider("test"): fixedProviderFactory(provider),
		},
	})
	if planDiags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}
	if len(plan.Steps) != 3 {
		t.Fatalf("expected 3 planned steps, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Name != "discover" {
		t.Fatalf("expected discover first, got %q", plan.Steps[0].Name)
	}
	if got := plan.Steps[0].Outputs.GetAttr("items").LengthInt(); got != 2 {
		t.Fatalf("expected discover items output length 2, got %d", got)
	}
	for _, step := range plan.Steps[1:] {
		if step.Name != "deploy" {
			t.Fatalf("expected repeated deploy instances, got %q", step.Name)
		}
		if step.InstanceKey == nil {
			t.Fatal("expected deploy instance key")
		}
		if step.RepetitionData == nil || step.RepetitionData.EachValue == cty.NilVal {
			t.Fatal("expected deploy repetition data")
		}
		if step.Status != runtime.StepStatusCompleted {
			t.Fatalf("expected deploy instance to complete planning, got %q", step.Status)
		}
	}
	if !provider.ListResourceCalled {
		t.Fatal("expected list resource to be listed during plan walk")
	}
}

func TestBuildPlanRejectsUnknownStepForEach(t *testing.T) {
	config := &runbookconfigs.RunbookConfig{
		Variables: map[string]*configs.Variable{
			"items": {Name: "items", Type: cty.DynamicPseudoType},
		},
		Steps: map[string]*runbookconfigs.Step{
			"deploy": {
				Name:    "deploy",
				ForEach: mustParseExpression(t, `var.items`),
			},
		},
	}

	plan, diags := BuildPlan(config, &PlannerOpts{})
	if plan != nil {
		t.Fatal("expected plan to be nil when step for_each is unknown")
	}
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for unknown step for_each")
	}
	if !strings.Contains(diags.Err().Error(), "Invalid for_each argument") {
		t.Fatalf("expected invalid for_each diagnostic, got: %s", diags.Err())
	}
}

func TestBuildPlanAllowsDataConsumerFromEarlierStepOutput(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

step "producer" {
  output "result" {
    value = "srv-123"
  }
}

step "consumer" {
  data "test_data" "target" {
    value = step.producer.result
  }

  output "final" {
    value = data.test_data.target.value
  }
}
`)

	config := loadIntegrationRunbookConfig(t, fs)

	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			DataSources: map[string]providers.Schema{
				"test_data": {
					Body: &configschema.Block{
						Attributes: map[string]*configschema.Attribute{
							"value": {Type: cty.String, Optional: true, Computed: true},
						},
					},
				},
			},
		},
		ReadDataSourceResponse: &providers.ReadDataSourceResponse{
			State: cty.ObjectVal(map[string]cty.Value{
				"value": cty.StringVal("srv-123"),
			}),
		},
	}

	plan, planDiags := BuildPlan(config, &PlannerOpts{
		Providers: map[addrs.Provider]providers.Factory{
			addrs.NewDefaultProvider("test"): fixedProviderFactory(provider),
		},
	})
	if planDiags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 planned steps, got %d", len(plan.Steps))
	}
	if got := plan.Steps[0].Outputs.GetAttr("result").AsString(); got != "srv-123" {
		t.Fatalf("expected producer output srv-123, got %q", got)
	}
	if got := plan.Steps[1].Outputs.GetAttr("final").AsString(); got != "srv-123" {
		t.Fatalf("expected final output srv-123, got %q", got)
	}
	if !provider.ReadDataSourceCalled {
		t.Fatal("expected data source read during plan")
	}
	if got := provider.ReadDataSourceRequest.Config.GetAttr("value").AsString(); got != "srv-123" {
		t.Fatalf("expected data source config to receive step output, got %q", got)
	}
}

func TestBuildPlanIgnoresPostconditions(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

variable "expected_runtime" {
  type    = string
  default = "python3.12"
}

step "inspect" {
  data "test_data" "target" {
    value = "python3.12"
  }

  output "runtime" {
    value = data.test_data.target.value
  }

  postcondition {
    condition     = data.test_data.target.value == var.expected_runtime
    error_message = "runtime mismatch"
  }
}
`)

	config := loadIntegrationRunbookConfig(t, fs)

	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			DataSources: map[string]providers.Schema{
				"test_data": {
					Body: &configschema.Block{
						Attributes: map[string]*configschema.Attribute{
							"value": {Type: cty.String, Optional: true, Computed: true},
						},
					},
				},
			},
		},
		ReadDataSourceResponse: &providers.ReadDataSourceResponse{
			State: cty.ObjectVal(map[string]cty.Value{
				"value": cty.StringVal("python3.12"),
			}),
		},
	}

	plan, planDiags := BuildPlan(config, &PlannerOpts{
		Providers: map[addrs.Provider]providers.Factory{
			addrs.NewDefaultProvider("test"): fixedProviderFactory(provider),
		},
	})
	if planDiags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 planned step, got %d", len(plan.Steps))
	}
	if got := plan.Steps[0].Outputs.GetAttr("runtime").AsString(); got != "python3.12" {
		t.Fatalf("expected runtime output python3.12, got %q", got)
	}
	if plan.Steps[0].Status != runtime.StepStatusCompleted {
		t.Fatalf("expected completed step during plan, got %q", plan.Steps[0].Status)
	}
}

func TestBuildPlanValidatesPostconditionsForExpressionErrors(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

step "inspect" {
  data "test_data" "target" {
    value = "python3.12"
  }

  output "runtime" {
    value = data.test_data.target.value
  }

  postcondition {
    condition     = data.test_data.target.missing == "python3.12"
    error_message = "runtime mismatch"
  }
}
`)

	config := loadIntegrationRunbookConfig(t, fs)

	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			DataSources: map[string]providers.Schema{
				"test_data": {
					Body: &configschema.Block{
						Attributes: map[string]*configschema.Attribute{
							"value": {Type: cty.String, Optional: true, Computed: true},
						},
					},
				},
			},
		},
		ReadDataSourceResponse: &providers.ReadDataSourceResponse{
			State: cty.ObjectVal(map[string]cty.Value{
				"value": cty.StringVal("python3.12"),
			}),
		},
	}

	_, planDiags := BuildPlan(config, &PlannerOpts{
		Providers: map[addrs.Provider]providers.Factory{
			addrs.NewDefaultProvider("test"): fixedProviderFactory(provider),
		},
	})
	if !planDiags.HasErrors() {
		t.Fatal("expected plan diagnostics for invalid postcondition expression")
	}
}

func TestBuildPlanDoesNotEnforceFalsePostconditions(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

step "inspect" {
  data "test_data" "target" {
    value = "python3.12"
  }

  output "runtime" {
    value = data.test_data.target.value
  }

  postcondition {
    condition     = data.test_data.target.value == "python3.11"
    error_message = "runtime mismatch"
  }
}
`)

	config := loadIntegrationRunbookConfig(t, fs)

	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			DataSources: map[string]providers.Schema{
				"test_data": {
					Body: &configschema.Block{
						Attributes: map[string]*configschema.Attribute{
							"value": {Type: cty.String, Optional: true, Computed: true},
						},
					},
				},
			},
		},
		ReadDataSourceResponse: &providers.ReadDataSourceResponse{
			State: cty.ObjectVal(map[string]cty.Value{
				"value": cty.StringVal("python3.12"),
			}),
		},
	}

	plan, planDiags := BuildPlan(config, &PlannerOpts{
		Providers: map[addrs.Provider]providers.Factory{
			addrs.NewDefaultProvider("test"): fixedProviderFactory(provider),
		},
	})
	if planDiags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 planned step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Status != runtime.StepStatusCompleted {
		t.Fatalf("expected completed step during plan, got %q", plan.Steps[0].Status)
	}
}

func writeIntegrationTestFile(t *testing.T, fs afero.Fs, path, src string) {
	t.Helper()
	if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %s", path, err)
	}
}
