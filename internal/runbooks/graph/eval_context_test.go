// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
)

func TestEvalContextVariableAccess(t *testing.T) {
	ctx := NewEvalContext(EvalContextOpts{})
	ctx.SetVariable("name", cty.StringVal("hello"))

	value, ok := ctx.GetVariable("name")
	if !ok {
		t.Fatal("expected variable to be available")
	}
	if value.AsString() != "hello" {
		t.Fatalf("wrong value %q", value.AsString())
	}
}

func TestEvalContextStepOutputAccess(t *testing.T) {
	ctx := NewEvalContext(EvalContextOpts{})
	ctx.SetStepOutput("deploy", cty.ObjectVal(map[string]cty.Value{
		"result": cty.StringVal("ok"),
	}))

	value, ok := ctx.GetStepOutput("deploy")
	if !ok {
		t.Fatal("expected step output to be available")
	}
	if !value.Type().IsObjectType() {
		t.Fatalf("expected object type, got %s", value.Type().FriendlyName())
	}
}

func TestEvalContextHCLContext(t *testing.T) {
	ctx := NewEvalContext(EvalContextOpts{})
	ctx.SetVariable("name", cty.StringVal("hello"))
	ctx.SetStepOutput("deploy", cty.ObjectVal(map[string]cty.Value{
		"result": cty.StringVal("ok"),
	}))

	expr, diags := hclsyntax.ParseExpression([]byte("step.deploy.result == \"ok\" && var.name == \"hello\""), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	hclCtx, moreDiags := ctx.HCLContext(expr)
	if moreDiags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", moreDiags.Err())
	}

	value, evalDiags := expr.Value(hclCtx)
	if evalDiags.HasErrors() {
		t.Fatalf("unexpected eval diagnostics: %s", evalDiags.Error())
	}
	if !value.True() {
		t.Fatal("expected expression to evaluate to true")
	}
}

func TestEvalContextWorkspaceConfig(t *testing.T) {
	config := &runbookconfig.RunbookConfig{}

	ctx := NewEvalContext(EvalContextOpts{Config: config})
	if ctx.Config() != config {
		t.Fatal("expected config accessor to return original config")
	}

	ctx = NewEvalContext(EvalContextOpts{Config: &runbookconfig.RunbookConfig{}})
	if ctx.WorkspaceConfig() != nil {
		t.Fatal("expected nil workspace config")
	}
}
