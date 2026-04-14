package addrs

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
)

func TestParseRefLocalValue(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `local.region`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	local, ok := ref.Subject.(terraformaddrs.LocalValue)
	if !ok {
		t.Fatalf("expected terraform LocalValue, got %T", ref.Subject)
	}
	if local.Name != "region" {
		t.Fatalf("unexpected local name: %s", local.Name)
	}
}

func TestParseRefStepOutput(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `step.deploy.result`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	output, ok := ref.Subject.(StepOutput)
	if !ok {
		t.Fatalf("expected StepOutput, got %T", ref.Subject)
	}
	if output.Step.StepName != "deploy" || output.OutputName != "result" {
		t.Fatalf("unexpected step output address: %s", output.String())
	}
}

func TestParseRefStepsOutput(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `steps.deploy.result`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	output, ok := ref.Subject.(StepOutput)
	if !ok {
		t.Fatalf("expected StepOutput, got %T", ref.Subject)
	}
	if output.Step.StepName != "deploy" || output.OutputName != "result" {
		t.Fatalf("unexpected step output address: %s", output.String())
	}
}

func TestParseRefWholeStep(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `steps.deploy`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	step, ok := ref.Subject.(Step)
	if !ok {
		t.Fatalf("expected Step, got %T", ref.Subject)
	}
	if step.Step.StepName != "deploy" {
		t.Fatalf("unexpected step name: %s", step.String())
	}
}

func TestParseRefWorkspaceAction(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `workspace.action.http.notify`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	action, ok := ref.Subject.(terraformaddrs.Action)
	if !ok {
		t.Fatalf("expected Action, got %T", ref.Subject)
	}
	if action.Type != "http" || action.Name != "notify" {
		t.Fatalf("unexpected workspace action address: %s", action.String())
	}
}

func mustParseTraversal(t *testing.T, src string) hcl.Traversal {
	t.Helper()
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte(src), "test.hcl", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		t.Fatalf("parse traversal %q: %s", src, diags.Error())
	}
	return traversal
}
