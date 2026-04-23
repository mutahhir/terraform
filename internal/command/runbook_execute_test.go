package command

import (
	"encoding/json"
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

output "summary" {
  value = step.discover.result
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
	if !strings.Contains(output.Stdout(), "Terraform will perform the following runbook steps:") {
		t.Fatalf("expected plan output before execute, got: %s", output.Stdout())
	}
	if strings.Index(output.Stdout(), "Terraform will perform the following runbook steps:") > strings.Index(output.Stdout(), "step.discover is in progress") {
		t.Fatalf("expected plan output to appear before execute events, got: %s", output.Stdout())
	}
	if got := provider.InvokeActionRequest.PlannedActionData.GetAttr("target").AsString(); got != "srv-123" {
		t.Fatalf("expected invoke action to receive planned target, got %q", got)
	}
	if !strings.Contains(output.Stdout(), "Runbook execution started.") {
		t.Fatalf("expected execution start header, got: %s", output.Stdout())
	}
	if !strings.Contains(output.Stdout(), "step.discover is in progress") {
		t.Fatalf("expected running step event, got: %s", output.Stdout())
	}
	if !strings.Contains(output.Stdout(), "action action.test_action.notify is running") {
		t.Fatalf("expected action running event, got: %s", output.Stdout())
	}
	if !strings.Contains(output.Stdout(), "action action.test_action.notify: invoking") {
		t.Fatalf("expected action progress event, got: %s", output.Stdout())
	}
	if !strings.Contains(output.Stdout(), "action action.test_action.notify completed") {
		t.Fatalf("expected action completed event, got: %s", output.Stdout())
	}
	if !strings.Contains(output.Stdout(), "step.discover completed") {
		t.Fatalf("expected completed step event, got: %s", output.Stdout())
	}
	if strings.Index(output.Stdout(), "action action.test_action.notify completed") > strings.Index(output.Stdout(), "step.discover completed") {
		t.Fatalf("expected step completion after action completion, got: %s", output.Stdout())
	}
	if !strings.Contains(output.Stdout(), "Runbook execute complete.") {
		t.Fatalf("expected execute summary, got: %s", output.Stdout())
	}
	if !strings.Contains(output.Stdout(), "Execution Report") {
		t.Fatalf("expected execution report, got: %s", output.Stdout())
	}
	if !strings.Contains(output.Stdout(), "Step 1: step.discover [Status: Complete] [Duration:") {
		t.Fatalf("expected step report entry with duration, got: %s", output.Stdout())
	}
	if !strings.Contains(output.Stdout(), "|   action: [1/1] action action.test_action.notify: invoking") {
		t.Fatalf("expected normalized step logs in execution report, got: %s", output.Stdout())
	}
	if !strings.Contains(output.Stdout(), "Outputs:") || !strings.Contains(output.Stdout(), `summary = "srv-123"`) {
		t.Fatalf("expected execute outputs in output, got: %s", output.Stdout())
	}
	if strings.Contains(output.Stdout(), `step.discover.result = "srv-123"`) {
		t.Fatalf("expected only top-level runbook outputs, got: %s", output.Stdout())
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
	if !strings.Contains(output.Stdout(), "step.discover failed") {
		t.Fatalf("expected failed step event, got: %s", output.Stdout())
	}
}

func TestRunbookExecuteCommandJSONEmitsEvents(t *testing.T) {
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

	code := c.Run([]string{"-json", "-auto-approve"})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, output.All())
	}
	lines := strings.Split(strings.TrimSpace(output.Stdout()), "\n")
	var found bool
	for _, line := range lines {
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg["type"] != "runbook_execute" {
			continue
		}
		found = true
		events, ok := msg["events"].([]any)
		if !ok || len(events) == 0 {
			t.Fatalf("expected execute events in output, got: %#v", msg)
		}
		actions, ok := msg["actions"].([]any)
		if !ok || len(actions) == 0 {
			t.Fatalf("expected action events in output, got: %#v", msg)
		}
		plan := msg["plan"].(map[string]any)
		steps := plan["steps"].([]any)
		if len(steps) != 1 {
			t.Fatalf("expected one executed step, got %#v", plan)
		}
		step := steps[0].(map[string]any)
		outputs, ok := step["outputs"].(map[string]any)
		if !ok || outputs["result"] != `"srv-123"` {
			t.Fatalf("expected step outputs in execute payload, got: %#v", step)
		}
	}
	if !found {
		t.Fatalf("expected runbook_execute message in output: %s", output.Stdout())
	}
}

func TestRunbookExecuteCommandJSONRequiresAutoApprove(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tf", ``)
	writeFile(t, td+"/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}
`)
	t.Chdir(td)

	view, done := testView(t)
	c := NewRunbookExecuteCommand(Meta{View: view})

	code := c.Run([]string{"-json"})
	output := done(t)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d: %s", code, output.All())
	}
	if !strings.Contains(output.All(), "Auto-approve required") {
		t.Fatalf("expected json approval diagnostic, got: %s", output.All())
	}
}

func TestRunbookExecuteCommandAllowsEmptyOptionalActionConfig(t *testing.T) {
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

step "summary" {
  action "test_action" "optional" {
    config {}
  }

  execute {
    invoke_action {
      action = action.test_action.optional
    }
  }
}
`)
	t.Chdir(td)

	view, done := testView(t)
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {
					ConfigSchema: &configschema.Block{Attributes: map[string]*configschema.Attribute{
						"name":  {Type: cty.String, Optional: true},
						"ratio": {Type: cty.Number, Optional: true},
						"color": {Type: cty.Bool, Optional: true},
					}},
				},
			},
		},
		PlanActionResponse: &providers.PlanActionResponse{},
		InvokeActionFn: func(req providers.InvokeActionRequest) providers.InvokeActionResponse {
			return providers.InvokeActionResponse{}
		},
	}
	c := NewRunbookExecuteCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})

	code := c.Run([]string{"-auto-approve", "-no-color"})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, output.All())
	}
	if !provider.InvokeActionCalled {
		t.Fatal("expected optional action config to invoke successfully")
	}
	if strings.Contains(output.All(), `attributes "color", "name", and "ratio" are required`) {
		t.Fatalf("unexpected required-attributes diagnostic: %s", output.All())
	}
	if got := provider.InvokeActionRequest.PlannedActionData.Type(); !got.IsObjectType() {
		t.Fatalf("expected schema-typed object planned config, got %s", got.FriendlyName())
	}
}

func TestRunbookExecuteCommandRepeatedStepsExecuteOnce(t *testing.T) {
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

step "invoke" {
  for_each = {
    primary = "srv-1"
    secondary = "srv-2"
  }

  action "test_action" "notify" {
    config {
      target = each.value
    }
  }

  execute {
    invoke_action {
      action = action.test_action.notify
    }
  }
}
`)
	t.Chdir(td)

	view, done := testView(t)
	count := 0
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {ConfigSchema: &configschema.Block{Attributes: map[string]*configschema.Attribute{"target": {Type: cty.String, Optional: true}}}},
			},
		},
		PlanActionResponse: &providers.PlanActionResponse{},
		InvokeActionFn: func(req providers.InvokeActionRequest) providers.InvokeActionResponse {
			count++
			return providers.InvokeActionResponse{}
		},
	}
	c := NewRunbookExecuteCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})

	code := c.Run([]string{"-auto-approve", "-no-color"})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, output.All())
	}
	if count != 2 {
		t.Fatalf("expected exactly 2 action invocations, got %d", count)
	}
}

func TestRunbookExecuteCommandWorkspaceActionAllowsEmptyOptionalConfig(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tf", `
action "test_action" "workspace_optional" {
  config {}
}
`)
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

step "summary" {
  execute {
    invoke_action {
      action = workspace.action.test_action.workspace_optional
    }
  }
}
`)
	t.Chdir(td)

	view, done := testView(t)
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {
					ConfigSchema: &configschema.Block{Attributes: map[string]*configschema.Attribute{
						"name":  {Type: cty.String, Optional: true},
						"ratio": {Type: cty.Number, Optional: true},
						"color": {Type: cty.Bool, Optional: true},
					}},
				},
			},
		},
		InvokeActionFn: func(req providers.InvokeActionRequest) providers.InvokeActionResponse {
			return providers.InvokeActionResponse{}
		},
	}
	c := NewRunbookExecuteCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})

	code := c.Run([]string{"-auto-approve", "-no-color"})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, output.All())
	}
	if !provider.InvokeActionCalled {
		t.Fatal("expected workspace action with optional config to invoke successfully")
	}
	if strings.Contains(output.All(), `attributes "color", "name", and "ratio" are required`) {
		t.Fatalf("unexpected required-attributes diagnostic: %s", output.All())
	}
}

func TestRunbookExecuteCommandWorkspaceActionUsesConfiguredOptionalAttrs(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tf", `
action "test_action" "workspace_optional" {
  config {
    name  = "bufo-the-builder"
    ratio = 0.15
    color = true
  }
}
`)
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

step "summary" {
  execute {
    invoke_action {
      action = workspace.action.test_action.workspace_optional
    }
  }
}
`)
	t.Chdir(td)

	view, done := testView(t)
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {
					ConfigSchema: &configschema.Block{Attributes: map[string]*configschema.Attribute{
						"name":  {Type: cty.String, Optional: true},
						"ratio": {Type: cty.Number, Optional: true},
						"color": {Type: cty.Bool, Optional: true},
					}},
				},
			},
		},
		InvokeActionFn: func(req providers.InvokeActionRequest) providers.InvokeActionResponse {
			return providers.InvokeActionResponse{}
		},
	}
	c := NewRunbookExecuteCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})

	code := c.Run([]string{"-auto-approve", "-no-color"})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, output.All())
	}
	if !provider.InvokeActionCalled {
		t.Fatal("expected configured workspace action to invoke successfully")
	}
	if got := provider.InvokeActionRequest.PlannedActionData.GetAttr("name").AsString(); got != "bufo-the-builder" {
		t.Fatalf("unexpected planned name %q", got)
	}
	if got := provider.InvokeActionRequest.PlannedActionData.GetAttr("color").True(); !got {
		t.Fatal("expected planned color=true")
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
			return providers.InvokeActionResponse{Events: func(yield func(providers.InvokeActionEvent) bool) {
				if !yield(providers.InvokeActionEvent_Progress{Message: "invoking"}) {
					return
				}
				yield(providers.InvokeActionEvent_Completed{})
			}}
		},
	}
	return provider
}
