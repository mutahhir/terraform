package command

import (
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
	if !strings.Contains(output.Stdout(), "Terraform will perform the following runbook steps:") {
		t.Fatalf("expected plan output, got: %s", output.Stdout())
	}
	stored, err := runbookplanfile.Read(planPath)
	if err != nil {
		t.Fatalf("read saved runbook plan: %s", err)
	}
	if len(stored.Sources) == 0 {
		t.Fatal("expected bundled runbook sources")
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
  value = one([for step in values(step.deploy) : step.summary if step.summary == "srv-123"])
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
