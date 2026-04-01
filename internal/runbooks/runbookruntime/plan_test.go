// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"testing"

	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/spf13/afero"
	"github.com/zclconf/go-cty/cty"
)

func TestRunbookContextPlanBuildsGraph(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "instance_count" {
  type    = number
  default = 2
}

step "deploy" {
  count = var.instance_count

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

	plan, planDiags := ctx.Plan(&PlanOpts{})
	if planDiags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}
	if plan == nil || plan.Graph == nil {
		t.Fatal("expected plan graph")
	}
	if len(plan.Graph.StepVertices) != 2 {
		t.Fatalf("expected 2 step vertices, got %#v", plan.Graph.StepVertices)
	}
}

func TestRunbookContextPlanUsesPlanTimeInputs(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "instance_count" {
  type    = number
  default = 1
}

step "deploy" {
  count = var.instance_count

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

	plan, planDiags := ctx.Plan(&PlanOpts{PlanTimeInputs: &PlanTimeInputs{
		Variables: map[string]cty.Value{
			"instance_count": cty.NumberIntVal(3),
		},
	}})
	if planDiags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}
	if plan == nil || plan.Graph == nil {
		t.Fatal("expected plan graph")
	}
	if len(plan.Graph.StepVertices) != 3 {
		t.Fatalf("expected 3 step vertices, got %#v", plan.Graph.StepVertices)
	}
}

func TestRunbookContextPlanRejectsUnknownStepRepetition(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "instance_count" {
  type = number
}

step "deploy" {
  count = var.instance_count

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

	plan, planDiags := ctx.Plan(&PlanOpts{})
	if !planDiags.HasErrors() {
		t.Fatal("expected plan diagnostics but got none")
	}
	if plan != nil {
		t.Fatalf("expected nil plan, got %#v", plan)
	}
}
