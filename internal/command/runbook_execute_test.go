package command

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/providers"
	testing_provider "github.com/hashicorp/terraform/internal/providers/testing"
	"github.com/zclconf/go-cty/cty"
)

func TestRunbookExecuteCommandInvokesPlannedActionConfig(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tf", ``)
	writeFile(t, td+"/main.tfrun.hcl", `
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
  data "test_data" "selected" {}

  action "test_action" "notify" {
    config {
      target = data.test_data.selected.id
    }
  }

  execute {
    invoke_action {
      action = action.test_action.notify
    }
  }

  output "result" {
    value = data.test_data.selected.id
  }
}
`)
	t.Chdir(td)

	view, done := testView(t)
	provider := runbookExecuteFixtureProvider()
	c := NewRunbookExecuteCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})

	code := c.Run([]string{"-auto-approve", "-no-color"})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, output.All())
	}
	if !provider.InvokeActionCalled {
		t.Fatal("expected action invocation during runbook execute")
	}
	if got := provider.InvokeActionRequest.PlannedActionData.GetAttr("target").AsString(); got != "srv-123" {
		t.Fatalf("expected invoke action to receive planned target, got %q", got)
	}
	if !strings.Contains(output.Stdout(), "Runbook execute complete.") {
		t.Fatalf("expected execute summary, got: %s", output.Stdout())
	}
}

func TestRunbookExecuteCommandFailsFalsePostcondition(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tf", ``)
	writeFile(t, td+"/main.tfrun.hcl", `
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
  data "test_data" "selected" {}

  action "test_action" "notify" {
    config {
      target = data.test_data.selected.id
    }
  }

  execute {
    invoke_action {
      action = action.test_action.notify
    }
  }

  postcondition {
    condition     = data.test_data.selected.id == "wrong"
    error_message = "postcondition failed"
  }

  output "result" {
    value = data.test_data.selected.id
  }
}
`)
	t.Chdir(td)

	view, done := testView(t)
	provider := runbookExecuteFixtureProvider()
	c := NewRunbookExecuteCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})

	code := c.Run([]string{"-auto-approve", "-no-color"})
	output := done(t)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d: %s", code, output.All())
	}
	if !strings.Contains(output.Stderr(), "postcondition failed") {
		t.Fatalf("expected postcondition failure, got: %s", output.Stderr())
	}
}

func runbookExecuteFixtureProvider() *testing_provider.MockProvider {
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {ConfigSchema: &configschema.Block{Attributes: map[string]*configschema.Attribute{"target": {Type: cty.String, Optional: true}}}},
			},
			DataSources: map[string]providers.Schema{
				"test_data": {Body: &configschema.Block{Attributes: map[string]*configschema.Attribute{"id": {Type: cty.String, Computed: true}}}},
			},
		},
		ReadDataSourceResponse: &providers.ReadDataSourceResponse{
			State: cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("srv-123")}),
		},
		PlanActionResponse: &providers.PlanActionResponse{},
		InvokeActionFn: func(req providers.InvokeActionRequest) providers.InvokeActionResponse {
			return providers.InvokeActionResponse{}
		},
	}
	return provider
}
