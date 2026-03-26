// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

func TestEvaluateStepForPlanSupportsTerraformBuiltins(t *testing.T) {
	rootDir := t.TempDir()

	file, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "example" {
  execute {}

  precondition {
    condition     = var.enabled && min(3, 7) == 3 && try(jsondecode("{\"ok\":true}").ok, false)
    error_message = "builtins should work"
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	step := file.Steps["example"]
	if step == nil {
		t.Fatal("expected step")
	}

	eval := EvaluateStepForPlan(step, EvalScope{Variables: cty.ObjectVal(map[string]cty.Value{"enabled": cty.True})})
	if got, want := eval.Status, StepStatusReady; got != want {
		t.Fatalf("wrong status: got %s want %s", got, want)
	}
	if eval.Diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", eval.Diags.Err())
	}
}

func TestEvaluateStepForPlanResolvesLocalsBeforePreconditions(t *testing.T) {
	rootDir := t.TempDir()

	file, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "example" {
  locals {
    summary_target = steps.discover.lambda_name
  }

  precondition {
    condition     = local.summary_target != ""
    error_message = "local should be available during precondition evaluation"
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	step := file.Steps["example"]
	if step == nil {
		t.Fatal("expected step")
	}

	eval := EvaluateStepForPlan(step, EvalScope{Steps: cty.ObjectVal(map[string]cty.Value{
		"discover": cty.ObjectVal(map[string]cty.Value{
			"lambda_name": cty.StringVal("runbook-scratchpad-ops-smoke"),
		}),
	})})
	if got, want := eval.Status, StepStatusReady; got != want {
		t.Fatalf("wrong status: got %s want %s; diags=%s", got, want, eval.Diags.Err())
	}
	if eval.Diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", eval.Diags.Err())
	}
}

func TestEvaluateStepForPlanDefersUnknownPreconditions(t *testing.T) {
	rootDir := t.TempDir()

	file, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "example" {
  precondition {
    condition     = var.enabled
    error_message = "should defer unknown"
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	step := file.Steps["example"]
	if step == nil {
		t.Fatal("expected step")
	}

	eval := EvaluateStepForPlan(step, EvalScope{Variables: cty.ObjectVal(map[string]cty.Value{"enabled": cty.UnknownVal(cty.Bool)})})
	if got, want := eval.Status, StepStatusReady; got != want {
		t.Fatalf("wrong status: got %s want %s; diags=%s", got, want, eval.Diags.Err())
	}
	if eval.Diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", eval.Diags.Err())
	}
}

func TestEvaluateStepForPlanSkipPreconditionsDoNotProduceErrors(t *testing.T) {
	rootDir := t.TempDir()

	file, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "example" {
  precondition {
    condition     = var.enabled
    error_message = "skip me"
    on_fail       = "skip"
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	step := file.Steps["example"]
	if step == nil {
		t.Fatal("expected step")
	}

	eval := EvaluateStepForPlan(step, EvalScope{Variables: cty.ObjectVal(map[string]cty.Value{"enabled": cty.False})})
	if got, want := eval.Status, StepStatusSkipped; got != want {
		t.Fatalf("wrong status: got %s want %s; diags=%s", got, want, eval.Diags.Err())
	}
	if eval.Diags.HasErrors() {
		t.Fatalf("skip precondition should not produce error diagnostics: %s", eval.Diags.Err())
	}
}
