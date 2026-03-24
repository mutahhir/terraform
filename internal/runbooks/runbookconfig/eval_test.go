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
