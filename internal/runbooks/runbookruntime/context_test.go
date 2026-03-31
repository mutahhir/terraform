// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"reflect"
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/spf13/afero"
)

func TestNewContext(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", `
output "root_value" { value = "hello" }

action "http" "notify" {}
`)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "name" {
  type = string
}

output "summary" {
  value = step.deploy.result
}

step "deploy" {
  locals {
    greeting = "hello"
  }

  data "aws_instance" "example" {}

  list "aws_instance" "example" {
    provider = aws
  }

  action "http" "notify" {}

  output "result" {
    value = "ok"
  }

  execute {
    invoke_action {
      action = workspace.action.http.notify
    }
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if ctx == nil {
		t.Fatal("expected context but got nil")
	}
	if ctx.Config() != config {
		t.Fatal("context did not retain config")
	}
	if got := ctx.Variable("name"); got == nil {
		t.Fatal("expected indexed runbook variable")
	}
	if got := ctx.Output("summary"); got == nil {
		t.Fatal("expected indexed runbook output")
	}
	if got := ctx.Step("deploy"); got == nil {
		t.Fatal("expected indexed step")
	}
	if got := ctx.StepLocal("deploy", "greeting"); got == nil {
		t.Fatal("expected indexed step local")
	}
	if got := ctx.StepAction("deploy", "http", "notify"); got == nil {
		t.Fatal("expected indexed step action")
	}
	if got := ctx.StepDataSource("deploy", "aws_instance", "example"); got == nil {
		t.Fatal("expected indexed step data source")
	}
	if got := ctx.StepList("deploy", "aws_instance", "example"); got == nil {
		t.Fatal("expected indexed step list")
	}
	if got := ctx.StepOutput("deploy", "result"); got == nil {
		t.Fatal("expected indexed step output")
	}
	wantActions := []runbookaddrs.ExecutableAction{
		runbookaddrs.WorkspaceActionInstance{Action: addrs.AbsAction{
			Module: addrs.RootModuleInstance,
			Action: addrs.Action{Type: "http", Name: "notify"},
		}, Key: addrs.NoKey},
	}
	if !reflect.DeepEqual(ctx.UsedWorkspaceActions(), wantActions) {
		t.Fatalf("wrong workspace actions\ngot:  %#v\nwant: %#v", ctx.UsedWorkspaceActions(), wantActions)
	}
	if len(ctx.UsedWorkspaceOutputs()) != 0 {
		t.Fatalf("expected no tracked workspace outputs, got %d", len(ctx.UsedWorkspaceOutputs()))
	}
	if diags := ctx.Validate(); diags.HasErrors() {
		t.Fatalf("unexpected validate diagnostics: %s", diags.Error())
	}
}

func TestNewContextNilConfig(t *testing.T) {
	ctx, diags := NewContext(&RunbookContextOpts{})
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
	if ctx != nil {
		t.Fatal("expected nil context")
	}
}

func TestRunbookContextValidateNil(t *testing.T) {
	var ctx *RunbookContext
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}

func TestRunbookContextValidateMissingStepAction(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  execute {
    invoke_action {
      action = action.http.notify
    }
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateMissingWorkspaceAction(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  execute {
    invoke_action {
      action = workspace.action.http.notify
    }
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func writeTestFile(t *testing.T, fs afero.Fs, path, src string) {
	t.Helper()
	if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %s", path, err)
	}
}
