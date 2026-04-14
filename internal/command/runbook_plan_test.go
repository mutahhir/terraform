package command

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/providers"
	testing_provider "github.com/hashicorp/terraform/internal/providers/testing"
	"github.com/zclconf/go-cty/cty"
)

func TestRunbookPlanCommand(t *testing.T) {
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
	provider := runbookPlanFixtureProvider()
	c := &RunbookPlanCommand{Meta: Meta{View: view, testingOverrides: metaOverridesForProvider(provider)}}

	code := c.Run([]string{"-no-color"})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, output.Stderr())
	}
	stdout := output.Stdout()
	if !strings.Contains(stdout, "Terraform will perform the following runbook steps:") {
		t.Fatalf("expected terraform-style plan header, got: %s", stdout)
	}
	if !strings.Contains(stdout, `# step.discover will be planned`) {
		t.Fatalf("expected planned step summary in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, `<= data "data.test_data.selected"`) {
		t.Fatalf("expected data read detail in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, `> execute "action.test_action.notify" with {`) {
		t.Fatalf("expected execute action detail in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, `"target" = "srv-123"`) {
		t.Fatalf("expected execute payload field in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, `Plan: 1 to run, 0 to skip.`) {
		t.Fatalf("expected runbook plan summary in output, got: %s", stdout)
	}
}

func TestRunbookPlanCommandJSON(t *testing.T) {
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

  output "result" {
    value = data.test_data.selected.id
  }
}
`)
	t.Chdir(td)

	view, done := testView(t)
	provider := runbookPlanFixtureProvider()
	c := &RunbookPlanCommand{Meta: Meta{View: view, testingOverrides: metaOverridesForProvider(provider)}}

	code := c.Run([]string{"-json"})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, output.All())
	}
	lines := strings.Split(strings.TrimSpace(output.Stdout()), "\n")
	if len(lines) == 0 {
		t.Fatal("expected json output")
	}
	var found bool
	for _, line := range lines {
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg["type"] == "runbook_plan" {
			found = true
			plan := msg["plan"].(map[string]any)
			steps := plan["steps"].([]any)
			if len(steps) != 1 {
				t.Fatalf("expected one planned step, got %#v", plan)
			}
		}
	}
	if !found {
		t.Fatalf("expected %q message in output: %s", "runbook_plan", output.Stdout())
	}
}

func TestRunbookPlanCommandShowsDiagnosticSnippet(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tf", ``)
	writeFile(t, td+"/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "discover" {
  precondition {
    condition     = false
    error_message = "this should show source"
  }
}
`)
	t.Chdir(td)

	view, done := testView(t)
	c := &RunbookPlanCommand{Meta: Meta{View: view}}

	code := c.Run([]string{"-no-color"})
	output := done(t)
	if code != 1 {
		t.Fatalf("unexpected exit code %d: %s", code, output.Stderr())
	}
	stderr := output.Stderr()
	if strings.Contains(stderr, "(source code not available)") {
		t.Fatalf("expected source snippet in diagnostic, got: %s", stderr)
	}
	if !strings.Contains(stderr, `condition     = false`) {
		t.Fatalf("expected source code snippet in diagnostic, got: %s", stderr)
	}
}

func TestRunbookPlanDiscoverRunbookPaths_CurrentDirIsWorkspace(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tf", ``)
	writeFile(t, td+"/main.tfrun.hcl", `step "discover" {}`)

	c := &RunbookPlanCommand{}
	runbookDir, workspaceDir, diags := c.discoverRunbookPaths(td)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if runbookDir != td {
		t.Fatalf("wrong runbook dir %q", runbookDir)
	}
	if workspaceDir != td {
		t.Fatalf("wrong workspace dir %q", workspaceDir)
	}
}

func TestRunbookPlanDiscoverRunbookPaths_NestedRunbookUsesNearestWorkspaceRoot(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tf", ``)
	if err := os.MkdirAll(td+"/runbooks/deploy", 0o755); err != nil {
		t.Fatalf("mkdir runbook dir: %s", err)
	}
	writeFile(t, td+"/runbooks/deploy/main.tfrun.hcl", `step "discover" {}`)

	c := &RunbookPlanCommand{}
	runbookDir, workspaceDir, diags := c.discoverRunbookPaths(td + "/runbooks/deploy")
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if runbookDir != td+"/runbooks/deploy" {
		t.Fatalf("wrong runbook dir %q", runbookDir)
	}
	if workspaceDir != td {
		t.Fatalf("wrong workspace dir %q", workspaceDir)
	}
}

func runbookPlanFixtureProvider() *testing_provider.MockProvider {
	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {ConfigSchema: &configschema.Block{Attributes: map[string]*configschema.Attribute{"target": {Type: cty.String, Optional: true}}}},
			},
			DataSources: map[string]providers.Schema{
				"test_data": {Body: &configschema.Block{}},
			},
		},
		ReadDataSourceResponse: &providers.ReadDataSourceResponse{
			State: cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("srv-123")}),
		},
	}
	return provider
}

func writeFile(t *testing.T, path, src string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %s", path, err)
	}
}
