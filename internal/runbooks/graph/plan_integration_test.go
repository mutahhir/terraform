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

	parser := runbookconfigs.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Error())
	}
	if config == nil {
		t.Fatal("expected config but got nil")
	}

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

	parser := runbookconfigs.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}
	if config == nil {
		t.Fatal("expected config but got nil")
	}

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

	parser := runbookconfigs.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}
	if config == nil {
		t.Fatal("expected config but got nil")
	}

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

func writeIntegrationTestFile(t *testing.T, fs afero.Fs, path, src string) {
	t.Helper()
	if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %s", path, err)
	}
}
