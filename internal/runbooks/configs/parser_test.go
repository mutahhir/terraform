package runbookconfigs

import (
	"testing"

	"github.com/spf13/afero"
)

func TestLoadRunbookConfigDir(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

variable "name" {
  type = string
}

output "summary" {
  value = step.deploy.result
}
`)
	writeTestFile(t, fs, "/runbook/steps/deploy.tfrun.hcl", `
step "deploy" {
  action "http" "notify" {}

  execute {
    invoke_action {
      action = action.http.notify
    }
  }
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Error())
	}
	if got == nil {
		t.Fatal("expected config but got nil")
	}
	if got.RunbookSourceDir != "/runbook" {
		t.Fatalf("wrong runbook dir %q", got.RunbookSourceDir)
	}
	if got.WorkspaceSourceDir != "" {
		t.Fatalf("expected workspace dir to be unset, got %q", got.WorkspaceSourceDir)
	}
	if got.WorkspaceConfig != nil {
		t.Fatal("expected workspace config to remain unset")
	}
	step, exists := got.Steps["deploy"]
	if !exists {
		t.Fatal("expected deploy step")
	}
	if len(step.Executions) != 1 {
		t.Fatalf("wrong execution count %d", len(step.Executions))
	}
	if len(step.Executions[0].InvokeAction) != 1 {
		t.Fatalf("wrong invoke_action count %d", len(step.Executions[0].InvokeAction))
	}
}

func TestLoadWorkspaceReferencesConfig(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", `
module "child" {
  source = "./child"
}

output "root_value" {
  value = "hello"
}

action "http" "notify" {}
`)
	writeTestFile(t, fs, "/workspace/child/main.tf", `
action "http" "child_notify" {}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadWorkspaceReferencesConfig("/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Error())
	}
	if got == nil || got.Module == nil {
		t.Fatal("expected workspace config to be loaded")
	}
	if _, exists := got.Module.Outputs["root_value"]; !exists {
		t.Fatal("expected workspace output root_value")
	}
	if _, exists := got.Module.Actions["action.http.notify"]; !exists {
		t.Fatal("expected workspace action action.http.notify")
	}
	child, exists := got.Children["child"]
	if !exists || child == nil || child.Module == nil {
		t.Fatal("expected child workspace module to be loaded")
	}
	if _, exists := child.Module.Actions["action.http.child_notify"]; !exists {
		t.Fatal("expected child workspace action action.http.child_notify")
	}
}

func TestLoadWorkspaceReferencesConfigNestedLocalModules(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", `
module "child" {
  source = "./child"
}
`)
	writeTestFile(t, fs, "/workspace/child/main.tf", `
module "grandchild" {
  source = "./grandchild"
}
`)
	writeTestFile(t, fs, "/workspace/child/grandchild/main.tf", `
output "grandchild_value" {
  value = "hello"
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadWorkspaceReferencesConfig("/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Error())
	}
	child := got.Children["child"]
	if child == nil {
		t.Fatal("expected child workspace module to be loaded")
	}
	grandchild := child.Children["grandchild"]
	if grandchild == nil || grandchild.Module == nil {
		t.Fatal("expected grandchild workspace module to be loaded")
	}
	if _, exists := grandchild.Module.Outputs["grandchild_value"]; !exists {
		t.Fatal("expected grandchild workspace output grandchild_value")
	}

	sources := map[string][]byte{
		"/workspace/main.tf":                 []byte(`module "child" { source = "./child" }`),
		"/workspace/child/main.tf":           []byte(`module "grandchild" { source = "./grandchild" }`),
		"/workspace/child/grandchild/main.tf": []byte(`output "grandchild_value" { value = "hello" }`),
	}
	got, diags = p.LoadWorkspaceReferencesConfigSources("/workspace", sources)
	if diags.HasErrors() {
		t.Fatalf("unexpected source diagnostics: %s", diags.Error())
	}
	child = got.Children["child"]
	if child == nil {
		t.Fatal("expected child workspace module to be reconstructed")
	}
	grandchild = child.Children["grandchild"]
	if grandchild == nil || grandchild.Module == nil {
		t.Fatal("expected grandchild workspace module to be reconstructed")
	}
	if _, exists := grandchild.Module.Outputs["grandchild_value"]; !exists {
		t.Fatal("expected reconstructed grandchild workspace output grandchild_value")
	}
}

func TestLoadRunbookConfigDirInvokeActionSyntax(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "deploy" {
  action "http" "notify" {}

  execute {
    invoke_action {
      action = action.http.notify
    }
  }
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Error())
	}
	if got == nil {
		t.Fatal("expected config but got nil")
	}
	step, exists := got.Steps["deploy"]
	if !exists {
		t.Fatal("expected deploy step")
	}
	if len(step.Executions) != 1 {
		t.Fatalf("wrong execution count %d", len(step.Executions))
	}
	if len(step.Executions[0].InvokeAction) != 1 {
		t.Fatalf("wrong invoke_action count %d", len(step.Executions[0].InvokeAction))
	}
}

func TestLoadRunbookConfigDirMissingTerraformVersion(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {}

step "deploy" {}
`)

	p := NewRunbookParser(fs)
	_, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}

func TestLoadRunbookConfigDirMissingRunbookBlock(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `step "deploy" {}`)

	p := NewRunbookParser(fs)
	_, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}

func TestLoadRunbookConfigDirDuplicateRunbookBlock(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/a.tfrun.hcl", `runbook { terraform_version = ">= 1.0.0" }`)
	writeTestFile(t, fs, "/runbook/b.tfrun.hcl", `runbook { terraform_version = ">= 1.0.0" }`)

	p := NewRunbookParser(fs)
	_, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}

func TestLoadRunbookConfigDirDuplicateStep(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/a.tfrun.hcl", `step "deploy" {}`)
	writeTestFile(t, fs, "/runbook/b.tfrun.hcl", `step "deploy" {}`)

	p := NewRunbookParser(fs)
	_, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}

func TestLoadRunbookConfigDirDuplicateStepLocal(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  locals {
    greeting = "hello"
    greeting = "goodbye"
  }
}
`)

	p := NewRunbookParser(fs)
	_, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}

func TestLoadRunbookConfigDirDuplicateStepAction(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  action "http" "notify" {}
  action "http" "notify" {}
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
	if got == nil {
		t.Fatal("expected config but got nil")
	}
	if step := got.Steps["deploy"]; step == nil {
		t.Fatal("expected deploy step")
	} else if len(step.Actions) != 1 {
		t.Fatalf("expected duplicate action to be excluded, got %d actions", len(step.Actions))
	}
}

func TestLoadRunbookConfigDirDuplicateStepData(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  data "aws_instance" "example" {}
  data "aws_instance" "example" {}
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
	if got == nil {
		t.Fatal("expected config but got nil")
	}
	if step := got.Steps["deploy"]; step == nil {
		t.Fatal("expected deploy step")
	} else if len(step.DataSources) != 1 {
		t.Fatalf("expected duplicate data source to be excluded, got %d data sources", len(step.DataSources))
	}
}

func TestLoadRunbookConfigDirDuplicateStepList(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  list "aws_instance" "example" {
    provider = aws
  }

  list "aws_instance" "example" {
    provider = aws
  }
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
	if got == nil {
		t.Fatal("expected config but got nil")
	}
	if step := got.Steps["deploy"]; step == nil {
		t.Fatal("expected deploy step")
	} else if len(step.ListResources) != 1 {
		t.Fatalf("expected duplicate list block to be excluded, got %d list blocks", len(step.ListResources))
	}
}

func TestLoadRunbookConfigDirDuplicateStepOutput(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  output "result" {
    value = "ok"
  }

  output "result" {
    value = "still ok"
  }
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
	if got == nil {
		t.Fatal("expected config but got nil")
	}
	if step := got.Steps["deploy"]; step == nil {
		t.Fatal("expected deploy step")
	} else if len(step.Outputs) != 1 {
		t.Fatalf("expected duplicate output to be excluded, got %d outputs", len(step.Outputs))
	}
}

func TestLoadRunbookConfigDirEmptyExecuteBlock(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "deploy" {
  execute {}
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
	if got == nil {
		t.Fatal("expected config but got nil")
	}
	step := got.Steps["deploy"]
	if step == nil {
		t.Fatal("expected deploy step")
	}
	if len(step.Executions) != 0 {
		t.Fatalf("expected empty execute block to be excluded, got %d executions", len(step.Executions))
	}
}

func TestLoadRunbookConfigDirInvalidStepConditionDoesNotPanic(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "deploy" {
  postcondition {
    expression = true
    message    = "unexpected"
  }
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
	if got == nil {
		t.Fatal("expected config but got nil")
	}
	step := got.Steps["deploy"]
	if step == nil {
		t.Fatal("expected deploy step")
	}
	if len(step.Postconditions) != 1 {
		t.Fatalf("expected one postcondition block, got %d", len(step.Postconditions))
	}
	if step.Postconditions[0].Condition != nil {
		t.Fatal("expected invalid condition expression to remain unset")
	}
	if step.Postconditions[0].ErrorMessage != nil {
		t.Fatal("expected invalid error_message expression to remain unset")
	}
}

func writeTestFile(t *testing.T, fs afero.Fs, path, src string) {
	t.Helper()
	if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %s", path, err)
	}
}

func TestStepDependsOnParsesStepReferences(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "build" {
  action "http" "trigger" {}
  execute {
    invoke_action {
      action = action.http.trigger
    }
  }
}

step "verify" {
  depends_on = [step.build]

  action "http" "check" {}
  execute {
    invoke_action {
      action = action.http.check
    }
  }
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %s", diags.Error())
	}
	verify := got.Steps["verify"]
	if verify == nil {
		t.Fatal("expected verify step")
	}
	if len(verify.DependsOn) != 1 {
		t.Fatalf("expected 1 depends_on entry, got %d", len(verify.DependsOn))
	}
	if verify.DependsOn[0].RootName() != "step" {
		t.Fatalf("expected depends_on root to be 'step', got %q", verify.DependsOn[0].RootName())
	}
}

func TestStepDependsOnRejectsNonStepReferences(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "build" {
  action "http" "trigger" {}
  execute {
    invoke_action {
      action = action.http.trigger
    }
  }
}

step "verify" {
  depends_on = [var.something]

  action "http" "check" {}
  execute {
    invoke_action {
      action = action.http.check
    }
  }
}
`)

	p := NewRunbookParser(fs)
	_, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected error for non-step depends_on reference")
	}
	found := false
	for _, d := range diags {
		if d.Summary == "Invalid depends_on reference" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'Invalid depends_on reference' diagnostic, got: %s", diags.Error())
	}
}

func TestStepWaitBlindModeParsesCorrectly(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "deploy" {
  action "http" "trigger" {}

  execute {
    invoke_action {
      action = action.http.trigger
    }

    wait "warmup" {
      duration = "30s"
    }
  }
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %s", diags.Error())
	}
	step := got.Steps["deploy"]
	if step == nil {
		t.Fatal("expected deploy step")
	}
	if len(step.Executions) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(step.Executions))
	}
	exec := step.Executions[0]
	if len(exec.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(exec.Operations))
	}
	waitOp := exec.Operations[1]
	if waitOp.Type != ExecuteOpWait {
		t.Fatalf("expected wait operation, got %s", waitOp.Type)
	}
	if waitOp.Wait.Name != "warmup" {
		t.Fatalf("expected wait name 'warmup', got %q", waitOp.Wait.Name)
	}
	if waitOp.Wait.Mode != WaitModeDuration {
		t.Fatalf("expected duration mode, got %s", waitOp.Wait.Mode)
	}
	if waitOp.Wait.Duration.Seconds() != 30 {
		t.Fatalf("expected 30s duration, got %s", waitOp.Wait.Duration)
	}
}

func TestStepWaitPollingModeParsesCorrectly(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "check" {
  data "http" "status" {}

  execute {
    wait "ready" {
      datasource   = data.http.status
      condition    = data.http.status.body != ""
      timeout      = "5m"
      interval     = "15s"
      max_attempts = 20
    }
  }
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %s", diags.Error())
	}
	step := got.Steps["check"]
	if step == nil {
		t.Fatal("expected check step")
	}
	exec := step.Executions[0]
	waitOp := exec.Operations[0]
	if waitOp.Wait.Mode != WaitModePolling {
		t.Fatalf("expected polling mode, got %s", waitOp.Wait.Mode)
	}
	if waitOp.Wait.Timeout.Minutes() != 5 {
		t.Fatalf("expected 5m timeout, got %s", waitOp.Wait.Timeout)
	}
	if waitOp.Wait.Interval.Seconds() != 15 {
		t.Fatalf("expected 15s interval, got %s", waitOp.Wait.Interval)
	}
	if waitOp.Wait.MaxAttempts != 20 {
		t.Fatalf("expected 20 max_attempts, got %d", waitOp.Wait.MaxAttempts)
	}
}

func TestStepWaitRejectsMixedModes(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "bad" {
  data "http" "status" {}

  execute {
    wait "conflict" {
      duration   = "30s"
      datasource = data.http.status
      condition  = true
      timeout    = "5m"
    }
  }
}
`)

	p := NewRunbookParser(fs)
	_, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected error for mixed duration and polling attributes")
	}
	found := false
	for _, d := range diags {
		if d.Summary == "Cannot combine duration with polling attributes" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected mixed-mode diagnostic, got: %s", diags.Error())
	}
}

func TestStepWaitRejectsMissingLimits(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "bad" {
  data "http" "status" {}

  execute {
    wait "nolimit" {
      datasource = data.http.status
      condition  = true
    }
  }
}
`)

	p := NewRunbookParser(fs)
	_, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected error for missing timeout/max_attempts")
	}
	found := false
	for _, d := range diags {
		if d.Summary == "Missing polling limit" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected missing-limit diagnostic, got: %s", diags.Error())
	}
}
