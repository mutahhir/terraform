package command

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/providers"
	testing_provider "github.com/hashicorp/terraform/internal/providers/testing"
	runbookplanfile "github.com/hashicorp/terraform/internal/runbooks/runbookplanfile"
	"github.com/zclconf/go-cty/cty"
)

func TestRunbookPlanCommandOutWritesSavedPlan(t *testing.T) {
	td, runbookDir := setupRunbookDir(t)
	writeFile(t, td+"/main.tf", ``)
	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
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
	t.Chdir(runbookDir)

	view, done := testView(t)
	provider := runbookExecuteFixtureProvider()
	cmd := NewRunbookPlanCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})
	planPath := filepath.Join(runbookDir, "saved.tfrunplan")

	code := cmd.Run([]string{"-no-color", "-out=" + planPath})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, output.All())
	}
	if !strings.Contains(output.Stdout(), "Runbook plan") {
		t.Fatalf("expected plan output, got: %s", output.Stdout())
	}
	stored, err := runbookplanfile.Read(planPath)
	if err != nil {
		t.Fatalf("read saved runbook plan: %s", err)
	}
	if len(stored.Sources) == 0 {
		t.Fatal("expected bundled runbook sources")
	}
	if len(stored.WorkspaceSources) == 0 {
		t.Fatal("expected bundled workspace sources")
	}
	if stored.WorkspaceSourceDir == "" {
		t.Fatal("expected bundled workspace source dir")
	}
	if len(stored.Steps) != 1 {
		t.Fatalf("expected one saved step, got %d", len(stored.Steps))
	}
	if got := stored.Steps[0].Outputs["result"]; got == nil {
		t.Fatal("expected saved step outputs")
	}
	if got := stored.Steps[0].Actions["action.test_action.notify"]; got == nil || got.PlannedConfig == nil {
		t.Fatalf("expected saved planned action config, got %#v", stored.Steps[0].Actions)
	}
}

func TestRunbookExecuteSavedPlanMatchesFreshExecute(t *testing.T) {
	freshTargets, freshOutput := runRunbookExecuteFixture(t, false)
	savedTargets, savedOutput := runRunbookExecuteFixture(t, true)

	if !reflect.DeepEqual(freshTargets, savedTargets) {
		t.Fatalf("invoke targets differ\nfresh: %v\nsaved: %v", freshTargets, savedTargets)
	}
	if !strings.Contains(freshOutput, `summary = "srv-123"`) {
		t.Fatalf("expected summary in fresh output, got: %s", freshOutput)
	}
	if !strings.Contains(savedOutput, `summary = "srv-123"`) {
		t.Fatalf("expected summary in saved-plan output, got: %s", savedOutput)
	}
}

func TestRunbookExecuteSavedPlanIsContainedToPlanFile(t *testing.T) {
	td, runbookDir := setupRunbookDir(t)
	writeFile(t, td+"/main.tf", ``)
	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
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

  action "test_action" "notify" {
    config {
      target = each.value.id
    }
  }

  execute {
    invoke_action {
      action = action.test_action.notify
    }
  }

  output "summary" {
    value = each.value.id
  }
}

output "summary" {
  value = one([for s in values(step.deploy) : s.summary if s.summary == "srv-123"])
}
`)
	writeDependencyLockFile(t, filepath.Join(runbookDir, runbookDependencyLockFilename), map[string]string{"test": "1.0.0"})
	writeDependencyLockFile(t, filepath.Join(td, dependencyLockFilename), map[string]string{"test": "1.0.0"})
	t.Chdir(runbookDir)

	var listCalls int
	var invokeTargets []string
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {ConfigSchema: &configschema.Block{Attributes: map[string]*configschema.Attribute{"target": {Type: cty.String, Optional: true}}}},
			},
			ListResourceTypes: map[string]providers.Schema{
				"test_list": {Body: &configschema.Block{}},
			},
		},
		ListResourceFn: func(req providers.ListResourceRequest) providers.ListResourceResponse {
			listCalls++
			return providers.ListResourceResponse{Result: cty.ObjectVal(map[string]cty.Value{
				"items": cty.TupleVal([]cty.Value{cty.StringVal("srv-123"), cty.StringVal("srv-456")}),
			})}
		},
		PlanActionResponse: &providers.PlanActionResponse{},
		InvokeActionFn: func(req providers.InvokeActionRequest) providers.InvokeActionResponse {
			invokeTargets = append(invokeTargets, req.PlannedActionData.GetAttr("target").AsString())
			return providers.InvokeActionResponse{Events: func(yield func(providers.InvokeActionEvent) bool) {
				yield(providers.InvokeActionEvent_Completed{})
			}}
		},
	}

	view, done := testView(t)
	planCmd := NewRunbookPlanCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})
	planPath := filepath.Join(runbookDir, "saved.tfrunplan")
	if code := planCmd.Run([]string{"-no-color", "-out=" + planPath}); code != 0 {
		output := done(t)
		t.Fatalf("unexpected plan exit code %d: %s", code, output.All())
	}
	done(t)
	if listCalls != 1 {
		t.Fatalf("expected one list call during planning, got %d", listCalls)
	}

	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
runbook {
  terraform_version = ">= 1.0.0"
}

step "broken" {
  for_each = var.does_not_exist
}
`)
	writeFile(t, filepath.Join(runbookDir, runbookDependencyLockFilename), `not a valid lock file`)
	writeFile(t, filepath.Join(td, dependencyLockFilename), `not a valid lock file`)
	provider.ListResourceFn = func(req providers.ListResourceRequest) providers.ListResourceResponse {
		listCalls++
		return providers.ListResourceResponse{Result: cty.ObjectVal(map[string]cty.Value{
			"items": cty.TupleVal([]cty.Value{cty.StringVal("srv-999")}),
		})}
	}

	view, done = testView(t)
	execCmd := NewRunbookExecuteCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})
	code := execCmd.Run([]string{"-no-color", planPath})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected execute exit code %d: %s", code, output.All())
	}
	if listCalls != 1 {
		t.Fatalf("expected saved-plan execute not to rerun list discovery; got %d list calls", listCalls)
	}
	sort.Strings(invokeTargets)
	if got, want := invokeTargets, []string{"srv-123", "srv-456"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("wrong invoke targets\ngot:  %v\nwant: %v", got, want)
	}
	if !strings.Contains(output.Stdout(), `summary = "srv-123"`) {
		t.Fatalf("expected top-level output from saved plan, got: %s", output.Stdout())
	}
}

func TestRunbookExecuteSavedPlanContainsWorkspaceConfigAndState(t *testing.T) {
	td, runbookDir := setupRunbookDir(t)
	writeFile(t, td+"/main.tf", `
terraform {
  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

data "test_data" "selected" {}

action "test_action" "workspace_ping" {
  config {
    target = "fixed-workspace-target"
  }
}
`)
	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
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
      action = workspace.action.test_action.workspace_ping
    }
  }

  output "region" {
    value = workspace.data.test_data.selected.id
  }
}

output "region" {
  value = step.summary.region
}
`)
	writeFile(t, td+"/terraform.tfstate", `
{
  "version": 4,
  "terraform_version": "1.16.0",
  "serial": 1,
  "lineage": "2c4f1c98-35d6-4ac0-a5df-c5cbf33de8f4",
  "outputs": {},
  "resources": [
    {
      "mode": "data",
      "type": "test_data",
      "name": "selected",
      "provider": "provider[\"registry.terraform.io/hashicorp/test\"]",
      "instances": [
        {
          "schema_version": 0,
          "attributes": {
            "id": "us-east-1"
          },
          "sensitive_attributes": [],
          "identity_schema_version": 0
        }
      ]
    }
  ],
  "check_results": null
}
`)
	writeDependencyLockFile(t, filepath.Join(runbookDir, runbookDependencyLockFilename), map[string]string{"test": "1.0.0"})
	writeDependencyLockFile(t, filepath.Join(td, dependencyLockFilename), map[string]string{"test": "1.0.0"})
	t.Chdir(runbookDir)

	var invokeTargets []string
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {ConfigSchema: &configschema.Block{Attributes: map[string]*configschema.Attribute{"target": {Type: cty.String, Optional: true}}}},
			},
		},
		InvokeActionFn: func(req providers.InvokeActionRequest) providers.InvokeActionResponse {
			invokeTargets = append(invokeTargets, req.PlannedActionData.GetAttr("target").AsString())
			return providers.InvokeActionResponse{Events: func(yield func(providers.InvokeActionEvent) bool) {
				yield(providers.InvokeActionEvent_Completed{})
			}}
		},
	}

	view, done := testView(t)
	planCmd := NewRunbookPlanCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})
	planPath := filepath.Join(runbookDir, "workspace-saved.tfrunplan")
	if code := planCmd.Run([]string{"-no-color", "-out=" + planPath}); code != 0 {
		output := done(t)
		t.Fatalf("unexpected plan exit code %d: %s", code, output.All())
	}
	done(t)

	writeFile(t, td+"/main.tf", `
output "workspace_region" {
  value = "eu-west-1"
}
`)
	writeFile(t, td+"/terraform.tfstate", `
{
  "version": 4,
  "terraform_version": "1.16.0",
  "serial": 2,
  "lineage": "2c4f1c98-35d6-4ac0-a5df-c5cbf33de8f4",
  "outputs": {},
  "resources": [
    {
      "mode": "data",
      "type": "test_data",
      "name": "selected",
      "provider": "provider[\"registry.terraform.io/hashicorp/test\"]",
      "instances": [
        {
          "schema_version": 0,
          "attributes": {
            "id": "eu-west-1"
          },
          "sensitive_attributes": [],
          "identity_schema_version": 0
        }
      ]
    }
  ],
  "check_results": null
}
`)
	writeFile(t, filepath.Join(td, dependencyLockFilename), `not a valid lock file`)

	view, done = testView(t)
	execCmd := NewRunbookExecuteCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})
	code := execCmd.Run([]string{"-no-color", planPath})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected execute exit code %d: %s", code, output.All())
	}
	if got, want := invokeTargets, []string{"fixed-workspace-target"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("wrong workspace action targets\ngot:  %v\nwant: %v", got, want)
	}
	if !strings.Contains(output.Stdout(), `region = "us-east-1"`) {
		t.Fatalf("expected saved workspace-backed output, got: %s", output.Stdout())
	}
	if strings.Contains(output.Stdout(), `region = "eu-west-1"`) {
		t.Fatalf("expected saved workspace state, got: %s", output.Stdout())
	}
}

func TestRunbookExecuteSavedPlanContainsWorkspaceModuleSourcesOutsideRoot(t *testing.T) {
	td, runbookDir := setupRunbookDir(t)
	sharedDir := filepath.Join(filepath.Dir(td), filepath.Base(td)+"-shared")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatalf("mkdir shared module dir: %s", err)
	}
	writeFile(t, td+"/main.tf", fmt.Sprintf(`
module "shared" {
  source = "../%s"
}
`, filepath.Base(sharedDir)))
	writeFile(t, filepath.Join(sharedDir, "main.tf"), `
output "region" {
  value = "us-east-1"
}
`)
	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
runbook {
  terraform_version = ">= 1.0.0"
}

output "region" {
  value = workspace.module.shared.output.region
}
`)
	t.Chdir(runbookDir)

	view, done := testView(t)
	planCmd := NewRunbookPlanCommand(Meta{View: view, testingOverrides: &testingOverrides{}})
	planPath := filepath.Join(runbookDir, "workspace-module-saved.tfrunplan")
	if code := planCmd.Run([]string{"-no-color", "-out=" + planPath}); code != 0 {
		output := done(t)
		t.Fatalf("unexpected plan exit code %d: %s", code, output.All())
	}
	done(t)

	stored, err := runbookplanfile.Read(planPath)
	if err != nil {
		t.Fatalf("failed to read saved plan: %s", err)
	}
	if _, exists := stored.WorkspaceSources[filepath.Join(sharedDir, "main.tf")]; !exists {
		t.Fatalf("expected saved plan to bundle external workspace module sources: %#v", stored.WorkspaceSources)
	}

	writeFile(t, td+"/main.tf", `invalid`)
	writeFile(t, filepath.Join(sharedDir, "main.tf"), `invalid`)

	view, done = testView(t)
	execCmd := NewRunbookExecuteCommand(Meta{View: view, testingOverrides: &testingOverrides{}})
	code := execCmd.Run([]string{"-no-color", planPath})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected execute exit code %d: %s", code, output.All())
	}
	if strings.Contains(output.All(), "Invalid runbook") || strings.Contains(output.All(), "Invalid expression") || strings.Contains(output.All(), "failed to read module directory") {
		t.Fatalf("expected execute to read the saved workspace module sources instead of disk, got: %s", output.All())
	}
}

func TestRunbookExecuteSavedPlanPreservesSkippedSteps(t *testing.T) {
	td, runbookDir := setupRunbookDir(t)
	writeFile(t, td+"/main.tf", ``)
	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

variable "should_run" {
  type    = bool
  default = false
}

provider "test" {}

step "skip_me" {
  precondition {
    condition     = var.should_run
    error_message = "planned skip"
    on_failure    = "skip"
  }

  action "test_action" "notify" {
    config {
      target = "should-not-run"
    }
  }

  execute {
    invoke_action {
      action = action.test_action.notify
    }
  }
}
`)
	t.Chdir(runbookDir)

	var invokeCalls int
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {ConfigSchema: &configschema.Block{Attributes: map[string]*configschema.Attribute{"target": {Type: cty.String, Optional: true}}}},
			},
		},
		PlanActionResponse: &providers.PlanActionResponse{},
		InvokeActionFn: func(req providers.InvokeActionRequest) providers.InvokeActionResponse {
			invokeCalls++
			return providers.InvokeActionResponse{}
		},
	}

	view, done := testView(t)
	planCmd := NewRunbookPlanCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})
	planPath := filepath.Join(runbookDir, "skipped.tfrunplan")
	if code := planCmd.Run([]string{"-no-color", "-out=" + planPath}); code != 0 {
		output := done(t)
		t.Fatalf("unexpected plan exit code %d: %s", code, output.All())
	}
	planOutput := done(t)
	if !strings.Contains(planOutput.Stdout(), `planned skip`) {
		t.Fatalf("expected skipped step in plan output, got: %s", planOutput.Stdout())
	}

	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
runbook {
  terraform_version = ">= 1.0.0"
}

step "skip_me" {}
`)

	view, done = testView(t)
	execCmd := NewRunbookExecuteCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})
	code := execCmd.Run([]string{"-no-color", planPath})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected execute exit code %d: %s", code, output.All())
	}
	if invokeCalls != 0 {
		t.Fatalf("expected skipped step to stay skipped, got %d invokes", invokeCalls)
	}
	if !strings.Contains(output.Stdout(), `planned skip`) {
		t.Fatalf("expected saved skip reason in execute output, got: %s", output.Stdout())
	}
}

func TestRunbookShowCommandReadsSavedPlanFile(t *testing.T) {
	td, runbookDir := setupRunbookDir(t)
	writeFile(t, td+"/main.tf", `
terraform {
  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

data "test_data" "selected" {}
`)
	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
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
  data "test_data" "selected" {}

  output "region" {
    value = workspace.data.test_data.selected.id
  }

  output "result" {
    value = data.test_data.selected.id
  }
}
`)
	writeFile(t, td+"/terraform.tfstate", `
{
  "version": 4,
  "terraform_version": "1.16.0",
  "serial": 1,
  "lineage": "2c4f1c98-35d6-4ac0-a5df-c5cbf33de8f4",
  "outputs": {},
  "resources": [
    {
      "mode": "data",
      "type": "test_data",
      "name": "selected",
      "provider": "provider[\"registry.terraform.io/hashicorp/test\"]",
      "instances": [
        {
          "schema_version": 0,
          "attributes": {
            "id": "us-east-1"
          },
          "sensitive_attributes": [],
          "identity_schema_version": 0
        }
      ]
    }
  ],
  "check_results": null
}
`)
	t.Chdir(runbookDir)

	provider := runbookExecuteFixtureProvider()
	view, done := testView(t)
	planCmd := NewRunbookPlanCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})
	planPath := filepath.Join(runbookDir, "showme.tfrunplan")
	if code := planCmd.Run([]string{"-no-color", "-out=" + planPath}); code != 0 {
		output := done(t)
		t.Fatalf("unexpected plan exit code %d: %s", code, output.All())
	}
	done(t)

	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `invalid`)
	writeFile(t, td+"/main.tf", `invalid`)
	writeFile(t, filepath.Join(runbookDir, runbookDependencyLockFilename), `not a valid lock file`)
	writeFile(t, filepath.Join(td, dependencyLockFilename), `not a valid lock file`)

	view, done = testView(t)
	showCmd := NewRunbookShowCommand(Meta{View: view})
	code := showCmd.Run([]string{"-no-color", planPath})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected show exit code %d: %s", code, output.All())
	}
	if !strings.Contains(output.Stdout(), "Saved runbook plan") {
		t.Fatalf("expected saved-plan header, got: %s", output.Stdout())
	}
	if !strings.Contains(output.Stdout(), "Steps:") {
		t.Fatalf("expected steps header, got: %s", output.Stdout())
	}
	if !strings.Contains(output.Stdout(), `reads:`) || !strings.Contains(output.Stdout(), `- data "data.test_data.selected"`) {
		t.Fatalf("expected hierarchical saved plan details, got: %s", output.Stdout())
	}
	if strings.Contains(output.All(), "Invalid runbook") {
		t.Fatalf("expected show to read the saved plan instead of disk, got: %s", output.All())
	}
}

func TestRunbookShowCommandJSONPrintsArtifact(t *testing.T) {
	td, runbookDir := setupRunbookDir(t)
	writeFile(t, td+"/main.tf", ``)
	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
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

  output "result" {
    value = data.test_data.selected.id
  }
}
`)
	t.Chdir(runbookDir)

	provider := runbookExecuteFixtureProvider()
	view, done := testView(t)
	planCmd := NewRunbookPlanCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})
	planPath := filepath.Join(runbookDir, "show-json.tfrunplan")
	if code := planCmd.Run([]string{"-no-color", "-out=" + planPath}); code != 0 {
		output := done(t)
		t.Fatalf("unexpected plan exit code %d: %s", code, output.All())
	}
	done(t)

	view, done = testView(t)
	showCmd := NewRunbookShowCommand(Meta{View: view})
	code := showCmd.Run([]string{"-json", planPath})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected show exit code %d: %s", code, output.All())
	}
	if strings.Contains(output.Stdout(), `"type":"runbook_show"`) || strings.Contains(output.Stdout(), `"type":"version"`) {
		t.Fatalf("expected bare artifact json, got: %s", output.Stdout())
	}
	var shown map[string]any
	if err := json.Unmarshal([]byte(output.Stdout()), &shown); err != nil {
		t.Fatalf("expected valid artifact json: %s\n%s", err, output.Stdout())
	}
	steps, ok := shown["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("expected one step in shown artifact, got: %#v", shown)
	}
	step := steps[0].(map[string]any)
	reads, ok := step["reads"].([]any)
	if !ok || len(reads) == 0 {
		t.Fatalf("expected hierarchical reads in shown artifact, got: %#v", shown)
	}
	outputs, ok := step["outputs"].(map[string]any)
	if !ok || outputs["result"] != `"srv-123"` {
		t.Fatalf("expected outputs in shown artifact, got: %#v", shown)
	}
}

func runRunbookExecuteFixture(t *testing.T, useSavedPlan bool) ([]string, string) {
	t.Helper()
	td, runbookDir := setupRunbookDir(t)
	writeFile(t, td+"/main.tf", ``)
	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
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
	t.Chdir(runbookDir)

	var invokeTargets []string
	provider := runbookExecuteFixtureProvider()
	provider.InvokeActionFn = func(req providers.InvokeActionRequest) providers.InvokeActionResponse {
		invokeTargets = append(invokeTargets, req.PlannedActionData.GetAttr("target").AsString())
		return providers.InvokeActionResponse{Events: func(yield func(providers.InvokeActionEvent) bool) {
			yield(providers.InvokeActionEvent_Completed{})
		}}
	}

	if useSavedPlan {
		view, done := testView(t)
		planCmd := NewRunbookPlanCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})
		planPath := filepath.Join(runbookDir, "saved.tfrunplan")
		if code := planCmd.Run([]string{"-no-color", "-out=" + planPath}); code != 0 {
			output := done(t)
			t.Fatalf("unexpected plan exit code %d: %s", code, output.All())
		}
		done(t)

		view, done = testView(t)
		execCmd := NewRunbookExecuteCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})
		if code := execCmd.Run([]string{"-no-color", planPath}); code != 0 {
			output := done(t)
			t.Fatalf("unexpected execute exit code %d: %s", code, output.All())
		}
		output := done(t)
		return invokeTargets, output.Stdout()
	}

	view, done := testView(t)
	execCmd := NewRunbookExecuteCommand(Meta{View: view, testingOverrides: metaOverridesForProvider(provider)})
	if code := execCmd.Run([]string{"-no-color", "-auto-approve"}); code != 0 {
		output := done(t)
		t.Fatalf("unexpected execute exit code %d: %s", code, output.All())
	}
	output := done(t)
	return invokeTargets, output.Stdout()
}
