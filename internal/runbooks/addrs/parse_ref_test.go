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
	if output.StepName != "deploy" || output.OutputName != "result" {
		t.Fatalf("unexpected step output address: %s", output.String())
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
