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

func TestRunbookContextValidateNestedActionConfigReferences(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "name" {
  type    = string
  default = "hello"
}

step "prepare" {
  output "result" {
    value = "ready"
  }
}

step "deploy" {
  locals {
    greeting = var.name
  }

  action "http" "notify" {
    config {
      message = local.greeting
      target  = step.prepare.result
    }
  }

  output "result" {
    value = "ok"
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
	if diags := ctx.Validate(); diags.HasErrors() {
		t.Fatalf("unexpected validation diagnostics: %s", diags.Err())
	}
	deps := ctx.StepDependencies("deploy")
	if len(deps) != 1 || deps[0] == nil || deps[0].Name() != "prepare" {
		t.Fatalf("wrong nested-config step dependencies\ngot: %#v", deps)
	}
}

func TestRunbookContextValidateNestedListConfigMissingReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  list "aws_instance" "example" {
    provider = aws

    config {
      filter = local.missing
    }
  }

  output "result" {
    value = "ok"
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

func TestRunbookContextValidateNestedConfigAllowsProviderRoot(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  list "aws_instance" "example" {
    provider = aws

    config {
      provider_ref = aws
    }
  }

  output "result" {
    value = "ok"
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
	if diags := ctx.Validate(); diags.HasErrors() {
		t.Fatalf("unexpected validation diagnostics: %s", diags.Err())
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

func TestRunbookContextValidateMissingStepOutput(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
output "summary" {
  value = step.deploy.result
}

step "deploy" {}
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

func TestRunbookContextValidateMissingWorkspaceOutput(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
output "summary" {
  value = workspace.output.root_value
}

step "deploy" {}
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

func TestRunbookContextValidateTracksWorkspaceOutputs(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", `
output "root_value" { value = "hello" }
`)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
output "summary" {
  value = workspace.output.root_value
}

step "deploy" {
  output "result" {
    value = workspace.output.root_value
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
	if diags := ctx.Validate(); diags.HasErrors() {
		t.Fatalf("unexpected validation diagnostics: %s", diags.Err())
	}
	want := []runbookaddrs.WorkspaceOutputValue{{
		Output: addrs.AbsOutputValue{
			Module:      addrs.RootModuleInstance,
			OutputValue: addrs.OutputValue{Name: "root_value"},
		},
	}}
	wantAll := append(append([]runbookaddrs.WorkspaceOutputValue{}, want...), want...)
	if !reflect.DeepEqual(ctx.UsedWorkspaceOutputs(), wantAll) {
		t.Fatalf("wrong workspace outputs\ngot:  %#v\nwant: %#v", ctx.UsedWorkspaceOutputs(), wantAll)
	}
	if !reflect.DeepEqual(ctx.StepWorkspaceOutputs("deploy"), want) {
		t.Fatalf("wrong step workspace outputs\ngot:  %#v\nwant: %#v", ctx.StepWorkspaceOutputs("deploy"), want)
	}
	if len(ctx.StepWorkspaceOutputs("missing")) != 0 {
		t.Fatalf("expected no workspace outputs for missing step, got %#v", ctx.StepWorkspaceOutputs("missing"))
	}
	if diags := ctx.Validate(); diags.HasErrors() {
		t.Fatalf("unexpected validation diagnostics on second validate: %s", diags.Err())
	}
	if !reflect.DeepEqual(ctx.UsedWorkspaceOutputs(), wantAll) {
		t.Fatalf("wrong workspace outputs after second validate\ngot:  %#v\nwant: %#v", ctx.UsedWorkspaceOutputs(), wantAll)
	}
	if !reflect.DeepEqual(ctx.StepWorkspaceOutputs("deploy"), want) {
		t.Fatalf("wrong step workspace outputs after second validate\ngot:  %#v\nwant: %#v", ctx.StepWorkspaceOutputs("deploy"), want)
	}
}

func TestRunbookContextValidateRejectsSelfStepOutputReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  output "result" {
    value = step.deploy.result
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
