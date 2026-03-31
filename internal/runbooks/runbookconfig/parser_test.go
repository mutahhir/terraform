package runbookconfig

import (
	"testing"

	"github.com/spf13/afero"
)

func TestLoadRunbookConfigDir(t *testing.T) {
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
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "name" {
  type = string
}

output "summary" {
  value = step.deploy.result
}

step "deploy" {
  action "http" "notify" {}

  execute {
    invoke_action {
      action = action.http.notify
    }
    invoke_action {
      action = workspace.action.http.notify
    }
  }
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Error())
	}
	if got == nil {
		t.Fatal("expected config but got nil")
	}
	if got.RunbookSourceDir != "/runbook" {
		t.Fatalf("wrong runbook dir %q", got.RunbookSourceDir)
	}
	if got.WorkspaceSourceDir != "/workspace" {
		t.Fatalf("wrong workspace dir %q", got.WorkspaceSourceDir)
	}
	if got.WorkspaceConfig == nil || got.WorkspaceConfig.Module == nil {
		t.Fatal("expected workspace config to be loaded")
	}
	if _, exists := got.WorkspaceConfig.Module.Outputs["root_value"]; !exists {
		t.Fatal("expected workspace output root_value")
	}
	if _, exists := got.WorkspaceConfig.Module.Actions["action.http.notify"]; !exists {
		t.Fatal("expected workspace action action.http.notify")
	}
	child, exists := got.WorkspaceConfig.Children["child"]
	if !exists || child == nil || child.Module == nil {
		t.Fatal("expected child workspace module to be loaded")
	}
	if _, exists := child.Module.Actions["action.http.child_notify"]; !exists {
		t.Fatal("expected child workspace action action.http.child_notify")
	}
	step, exists := got.Steps["deploy"]
	if !exists {
		t.Fatal("expected deploy step")
	}
	if len(step.Executions) != 1 {
		t.Fatalf("wrong execution count %d", len(step.Executions))
	}
	if len(step.Executions[0].InvokeAction) != 2 {
		t.Fatalf("wrong invoke_action count %d", len(step.Executions[0].InvokeAction))
	}
}

func TestLoadRunbookConfigDirDuplicateStep(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/a.tfrun.hcl", `step "deploy" {}`)
	writeTestFile(t, fs, "/runbook/b.tfrun.hcl", `step "deploy" {}`)

	p := NewRunbookParser(fs)
	_, diags := p.LoadRunbookConfigDir("/runbook", "/workspace")
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
	_, diags := p.LoadRunbookConfigDir("/runbook", "/workspace")
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
	got, diags := p.LoadRunbookConfigDir("/runbook", "/workspace")
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
	got, diags := p.LoadRunbookConfigDir("/runbook", "/workspace")
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
	got, diags := p.LoadRunbookConfigDir("/runbook", "/workspace")
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
	got, diags := p.LoadRunbookConfigDir("/runbook", "/workspace")
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

func writeTestFile(t *testing.T, fs afero.Fs, path, src string) {
	t.Helper()
	if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %s", path, err)
	}
}
