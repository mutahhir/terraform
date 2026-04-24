package command

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/depsfile"
	"github.com/hashicorp/terraform/internal/getproviders/providerreqs"
	"github.com/hashicorp/terraform/internal/providers"
	testing_provider "github.com/hashicorp/terraform/internal/providers/testing"
	"github.com/zclconf/go-cty/cty"
)

func TestRunbookPlanCommand(t *testing.T) {
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
	provider := runbookPlanFixtureProvider()
	c := &RunbookPlanCommand{runbookCommandBase: runbookCommandBase{Meta: Meta{View: view, testingOverrides: metaOverridesForProvider(provider)}}}

	code := c.Run([]string{"-no-color"})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, output.Stderr())
	}
	stdout := output.Stdout()
	if !strings.Contains(stdout, "Runbook plan") {
		t.Fatalf("expected runbook plan header, got: %s", stdout)
	}
	if !strings.Contains(stdout, "Steps:") {
		t.Fatalf("expected steps header, got: %s", stdout)
	}
	if !strings.Contains(stdout, `- step.discover (planned)`) {
		t.Fatalf("expected planned step summary in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, `reads:`) || !strings.Contains(stdout, `- data "data.test_data.selected"`) {
		t.Fatalf("expected hierarchical reads section in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, `actions:`) || !strings.Contains(stdout, `- action.test_action.notify`) {
		t.Fatalf("expected hierarchical actions section in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, `executions:`) || !strings.Contains(stdout, `- invoke action.test_action.notify`) {
		t.Fatalf("expected hierarchical executions section in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, `outputs:`) || !strings.Contains(stdout, `- result = "srv-123"`) {
		t.Fatalf("expected outputs section in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, `"target" = "srv-123"`) {
		t.Fatalf("expected action config in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, `Plan: 1 to run, 0 to skip.`) {
		t.Fatalf("expected runbook plan summary in output, got: %s", stdout)
	}
}

func TestRunbookPlanCommandJSON(t *testing.T) {
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

	view, done := testView(t)
	provider := runbookPlanFixtureProvider()
	c := &RunbookPlanCommand{runbookCommandBase: runbookCommandBase{Meta: Meta{View: view, testingOverrides: metaOverridesForProvider(provider)}}}

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
			info := plan["info"].([]any)
			var entry map[string]any
			for _, raw := range info {
				candidate := raw.(map[string]any)
				if candidate["type"] == "data" && candidate["subject"] == "data.test_data.selected" {
					entry = candidate
					break
				}
			}
			if entry == nil {
				t.Fatalf("expected data info entry, got %#v", plan)
			}
			details, ok := entry["details"].(map[string]any)
			if !ok {
				t.Fatalf("expected structured details in plan info, got %#v", entry)
			}
			if details["provider"] != "hashicorp/test" {
				t.Fatalf("unexpected provider details %#v", details)
			}
		}
	}
	if !found {
		t.Fatalf("expected %q message in output: %s", "runbook_plan", output.Stdout())
	}
}

func TestRunbookPlanCommandShowsDiagnosticSnippet(t *testing.T) {
	td, runbookDir := setupRunbookDir(t)
	writeFile(t, td+"/main.tf", ``)
	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
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
	t.Chdir(runbookDir)

	view, done := testView(t)
	c := &RunbookPlanCommand{runbookCommandBase: runbookCommandBase{Meta: Meta{View: view}}}

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

func TestRunbookPlanCommandLoadsWorkspaceStateForWorkspaceDataRefs(t *testing.T) {
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

step "discover" {
  output "result" {
    value = workspace.data.test_data.selected.id
  }
}
`)
	stateSrc := `
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
            "id": "srv-123"
          },
          "sensitive_attributes": [],
          "identity_schema_version": 0
        }
      ]
    }
  ],
  "check_results": null
}
`
	writeFile(t, td+"/terraform.tfstate", stateSrc)
	t.Chdir(runbookDir)

	view, done := testView(t)
	provider := runbookPlanFixtureProvider()
	c := &RunbookPlanCommand{runbookCommandBase: runbookCommandBase{Meta: Meta{View: view, testingOverrides: metaOverridesForProvider(provider)}}}

	code := c.Run([]string{"-no-color"})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, output.All())
	}
	if strings.Contains(output.All(), "Missing workspace state object") {
		t.Fatalf("expected workspace state-backed reference to resolve, got: %s", output.All())
	}
	if !strings.Contains(output.Stdout(), `- step.discover (planned)`) {
		t.Fatalf("expected planned step summary in output, got: %s", output.Stdout())
	}
}

func TestRunbookPlanCommandJSONIncludesWorkspaceReadInfo(t *testing.T) {
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

resource "test_resource" "selected" {}
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

step "discover" {
  output "result" {
    value = workspace.test_resource.selected.id
  }
}
`)
	stateSrc := `
{
  "version": 4,
  "terraform_version": "1.16.0",
  "serial": 1,
  "lineage": "0c58b738-4c90-4d6e-bdbf-a5dc4e0c0db4",
  "outputs": {},
  "resources": [
    {
      "mode": "managed",
      "type": "test_resource",
      "name": "selected",
      "provider": "provider[\"registry.terraform.io/hashicorp/test\"]",
      "instances": [
        {
          "schema_version": 0,
          "attributes": {
            "id": "srv-123"
          },
          "sensitive_attributes": [],
          "identity_schema_version": 0
        }
      ]
    }
  ],
  "check_results": null
}
`
	writeFile(t, td+"/terraform.tfstate", stateSrc)
	t.Chdir(runbookDir)

	view, done := testView(t)
	provider := runbookPlanFixtureProvider()
	c := &RunbookPlanCommand{runbookCommandBase: runbookCommandBase{Meta: Meta{View: view, testingOverrides: metaOverridesForProvider(provider)}}}

	code := c.Run([]string{"-json"})
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
		if msg["type"] != "runbook_plan" {
			continue
		}
		plan := msg["plan"].(map[string]any)
		info := plan["info"].([]any)
		for _, raw := range info {
			entry := raw.(map[string]any)
			if entry["type"] != "workspace_read" || entry["subject"] != "workspace.test_resource.selected" {
				continue
			}
			found = true
			details := entry["details"].(map[string]any)
			if details["kind"] != "resource" || details["provider"] != "hashicorp/test" || details["source"] != "workspace_state" {
				t.Fatalf("unexpected workspace read details %#v", details)
			}
			attrs := details["attributes"].([]any)
			if len(attrs) != 1 || attrs[0] != "id" {
				t.Fatalf("unexpected workspace read attributes %#v", attrs)
			}
		}
	}
	if !found {
		t.Fatalf("expected workspace_read info in output: %s", output.Stdout())
	}
}

func TestRunbookPlanCommandLoadsWorkspaceStateForWorkspaceResourceRefs(t *testing.T) {
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

resource "test_resource" "selected" {}
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

step "discover" {
  output "result" {
    value = workspace.test_resource.selected.id
  }
}
`)
	stateSrc := `
{
  "version": 4,
  "terraform_version": "1.16.0",
  "serial": 1,
  "lineage": "4b9871c8-8d32-4a4c-b38d-f8c6aee8d954",
  "outputs": {},
  "resources": [
    {
      "mode": "managed",
      "type": "test_resource",
      "name": "selected",
      "provider": "provider[\"registry.terraform.io/hashicorp/test\"]",
      "instances": [
        {
          "schema_version": 0,
          "attributes": {
            "id": "srv-123"
          },
          "sensitive_attributes": [],
          "identity_schema_version": 0
        }
      ]
    }
  ],
  "check_results": null
}
`
	writeFile(t, td+"/terraform.tfstate", stateSrc)
	t.Chdir(runbookDir)

	view, done := testView(t)
	provider := runbookPlanFixtureProvider()
	c := &RunbookPlanCommand{runbookCommandBase: runbookCommandBase{Meta: Meta{View: view, testingOverrides: metaOverridesForProvider(provider)}}}

	code := c.Run([]string{"-no-color"})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, output.All())
	}
	if strings.Contains(output.All(), "Missing workspace state object") {
		t.Fatalf("expected workspace state-backed resource reference to resolve, got: %s", output.All())
	}
	if !strings.Contains(output.Stdout(), `- workspace resource "workspace.test_resource.selected" attributes=[id]`) {
		t.Fatalf("expected workspace read detail in output, got: %s", output.Stdout())
	}
	if !strings.Contains(output.Stdout(), `- step.discover (planned)`) {
		t.Fatalf("expected planned step summary in output, got: %s", output.Stdout())
	}
}

func TestRunbookPlanDiscoverRunbookPaths_RejectsWorkspaceRootRunbook(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tf", ``)
	writeFile(t, td+"/main.tfrun.hcl", `step "discover" {}`)

	c := &RunbookPlanCommand{}
	_, _, diags := c.discoverRunbookPaths(td)
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
	if got := diags.Err().Error(); !strings.Contains(got, "Runbook directory must not be the workspace root") {
		t.Fatalf("unexpected diagnostics: %s", got)
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

func TestRunbookPlanDiscoverRunbookPaths_AllowsNestedRunbookConfigFiles(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tf", ``)
	if err := os.MkdirAll(td+"/runbooks/deploy/steps", 0o755); err != nil {
		t.Fatalf("mkdir runbook dir: %s", err)
	}
	writeFile(t, td+"/runbooks/deploy/steps/deploy.tfrun.hcl", `step "discover" {}`)

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
			ResourceTypes: map[string]providers.Schema{
				"test_resource": {Body: &configschema.Block{Attributes: map[string]*configschema.Attribute{"id": {Type: cty.String, Computed: true}}}},
			},
			DataSources: map[string]providers.Schema{
				"test_data": {Body: &configschema.Block{Attributes: map[string]*configschema.Attribute{"id": {Type: cty.String, Computed: true}}}},
			},
		},
		ReadDataSourceResponse: &providers.ReadDataSourceResponse{
			State: cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("srv-123")}),
		},
	}
	return provider
}

func TestRunbookProviderFactoriesIgnoreTerraformLockFile(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tfrun.hcl", `runbook { terraform_version = ">= 1.0.0" }`)
	writeFile(t, td+"/.terraform.lock.hcl", `this is not a valid lock file`)

	view, _ := testView(t)
	c := &RunbookPlanCommand{runbookCommandBase: runbookCommandBase{Meta: Meta{View: view, testingOverrides: metaOverridesForProvider(runbookPlanFixtureProvider())}}}

	factories, err := c.runbookProviderFactories(td)
	if err != nil {
		t.Fatalf("expected runbook-specific lock loading to ignore .terraform.lock.hcl: %s", err)
	}
	if _, ok := factories[addrs.NewDefaultProvider("test")]; !ok {
		t.Fatal("expected testing override provider factory to be available")
	}
}

func TestRunbookMetaUsesTfrunDataDir(t *testing.T) {
	td := t.TempDir()
	view, _ := testView(t)
	c := &RunbookPlanCommand{runbookCommandBase: runbookCommandBase{Meta: Meta{View: view}}}

	meta := c.runbookMeta(td)
	got := filepath.ToSlash(meta.providerLocalCacheDir().BasePath())
	want := filepath.ToSlash(filepath.Join(td, runbookDataDirName, "providers"))
	if got != want {
		t.Fatalf("wrong runbook provider cache dir %q, want %q", got, want)
	}
}

func TestRunbookPlanCommandFailsForUndeclaredRunbookProvider(t *testing.T) {
	td, runbookDir := setupRunbookDir(t)
	writeFile(t, td+"/main.tf", ``)
	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
runbook {
  terraform_version = ">= 1.0.0"
}

step "discover" {
  data "test_data" "selected" {}
}
`)
	t.Chdir(runbookDir)

	view, done := testView(t)
	c := &RunbookPlanCommand{runbookCommandBase: runbookCommandBase{Meta: Meta{View: view}}}

	code := c.Run([]string{"-no-color"})
	output := done(t)
	if code != 1 {
		t.Fatalf("expected failure exit code, got %d: %s", code, output.All())
	}
	if !strings.Contains(output.Stderr(), "Undeclared runbook provider") {
		t.Fatalf("expected undeclared provider diagnostic, got: %s", output.All())
	}
}

func TestRunbookPlanCommandDetectsStaleRunbookDependencyMetadata(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tf", ``)
	runbookDir := filepath.Join(td, "runbooks", "deploy")
	if err := os.MkdirAll(runbookDir, 0o755); err != nil {
		t.Fatalf("mkdir runbook dir: %s", err)
	}
	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source  = "hashicorp/test"
      version = "1.0.0"
    }
  }
}

provider "test" {}
`)
	t.Chdir(runbookDir)
	providerSource, close := newMockProviderSource(t, map[string][]string{"test": {"1.0.0"}})
	defer close()
	view, done := testView(t)
	initCmd := NewRunbookInitCommand(Meta{View: view, ProviderSource: providerSource})
	if code := initCmd.Run([]string{"-no-color"}); code != 0 {
		output := done(t)
		t.Fatalf("unexpected init exit code %d: %s", code, output.All())
	}
	done(t)

	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source  = "hashicorp/test"
      version = "2.0.0"
    }
  }
}

provider "test" {}
`)

	view, done = testView(t)
	planCmd := &RunbookPlanCommand{runbookCommandBase: runbookCommandBase{Meta: Meta{View: view}}}
	code := planCmd.Run([]string{"-no-color"})
	output := done(t)
	if code != 1 {
		t.Fatalf("expected failure exit code, got %d: %s", code, output.All())
	}
	if !strings.Contains(output.Stderr(), "Runbook dependencies are out of date") {
		t.Fatalf("expected stale dependency diagnostic, got: %s", output.All())
	}
}

func TestRunbookPlanCommandDetectsStaleWorkspaceDependencyMetadata(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tf", `
terraform {
  required_providers {
    test = {
      source  = "hashicorp/test"
      version = "1.0.0"
    }
  }
}

provider "test" {}

resource "test_resource" "selected" {}
`)
	writeDependencyLockFile(t, filepath.Join(td, dependencyLockFilename), map[string]string{"test": "1.0.0"})
	runbookDir := filepath.Join(td, "runbooks", "deploy")
	if err := os.MkdirAll(runbookDir, 0o755); err != nil {
		t.Fatalf("mkdir runbook dir: %s", err)
	}
	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
runbook {
  terraform_version = ">= 1.0.0"
}

step "discover" {
  output "result" {
    value = workspace.test_resource.selected.id
  }
}
`)
	t.Chdir(runbookDir)
	providerSource, close := newMockProviderSource(t, map[string][]string{"test": {"1.0.0", "2.0.0"}})
	defer close()
	view, done := testView(t)
	initCmd := NewRunbookInitCommand(Meta{View: view, ProviderSource: providerSource})
	if code := initCmd.Run([]string{"-no-color"}); code != 0 {
		output := done(t)
		t.Fatalf("unexpected init exit code %d: %s", code, output.All())
	}
	done(t)

	writeDependencyLockFile(t, filepath.Join(td, dependencyLockFilename), map[string]string{"test": "2.0.0"})

	view, done = testView(t)
	planCmd := &RunbookPlanCommand{runbookCommandBase: runbookCommandBase{Meta: Meta{View: view}}}
	code := planCmd.Run([]string{"-no-color"})
	output := done(t)
	if code != 1 {
		t.Fatalf("expected failure exit code, got %d: %s", code, output.All())
	}
	if !strings.Contains(output.Stderr(), "Runbook dependencies are out of date") {
		t.Fatalf("expected stale dependency diagnostic, got: %s", output.All())
	}
}

func setupRunbookDir(t *testing.T) (string, string) {
	t.Helper()
	workspaceDir := t.TempDir()
	runbookDir := filepath.Join(workspaceDir, "runbooks", "deploy")
	if err := os.MkdirAll(runbookDir, 0o755); err != nil {
		t.Fatalf("mkdir runbook dir: %s", err)
	}
	return workspaceDir, runbookDir
}

func writeDependencyLockFile(t *testing.T, path string, versions map[string]string) {
	t.Helper()
	locks := depsfile.NewLocks()
	for name, version := range versions {
		locks.SetProvider(
			addrs.NewDefaultProvider(name),
			providerreqs.MustParseVersion(version),
			providerreqs.MustParseVersionConstraints("="+version),
			nil,
		)
	}
	if diags := depsfile.SaveLocksToFile(locks, path); diags.HasErrors() {
		t.Fatalf("write lock file %s: %s", path, diags.Err())
	}
}

func writeFile(t *testing.T, path, src string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %s", path, err)
	}
}
