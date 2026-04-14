// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"testing"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/zclconf/go-cty/cty"
)

func TestEvalContextVariableAccess(t *testing.T) {
	ctx := NewEvalContext(EvalContextOpts{})

	ctx.SetVariable("name", &terraform.InputValue{
		Value: cty.StringVal("hello"),
	})

	value, ok := ctx.GetVariable("name")
	if !ok {
		t.Fatal("expected variable to be available")
	}
	if value == nil {
		t.Fatal("expected variable value to be non-nil")
	}
	if got := value.Value.AsString(); got != "hello" {
		t.Fatalf("wrong value %q", got)
	}
}

func TestEvalContextWorkspaceConfig(t *testing.T) {
	config := &runbookconfigs.RunbookConfig{
		WorkspaceConfig: &configs.Config{},
	}

	ctx := NewEvalContext(EvalContextOpts{Config: config})
	if ctx.Config() != config {
		t.Fatal("expected config accessor to return original config")
	}
	if ctx.WorkspaceConfig() != config.WorkspaceConfig {
		t.Fatal("expected workspace config accessor to return original workspace config")
	}

	ctx = NewEvalContext(EvalContextOpts{Config: &runbookconfigs.RunbookConfig{}})
	if ctx.WorkspaceConfig() != nil {
		t.Fatal("expected nil workspace config")
	}

	ctx = NewEvalContext(EvalContextOpts{})
	if ctx.WorkspaceConfig() != nil {
		t.Fatal("expected nil workspace config when no runbook config is set")
	}
}

func TestEvalContextExpressionVariablesIgnoreUnsetInputPlaceholders(t *testing.T) {
	config := &runbookconfigs.RunbookConfig{
		Variables: map[string]*configs.Variable{
			"name": {
				Name:    "name",
				Default: cty.StringVal("default-name"),
			},
		},
	}

	ctx := NewEvalContext(EvalContextOpts{Config: config})
	ctx.SetVariable("name", &terraform.InputValue{Value: cty.NilVal})

	vars := ctx.expressionVariables("")
	got := vars["var"].GetAttr("name")
	if got != cty.StringVal("default-name") {
		t.Fatalf("wrong variable value %#v", got)
	}
}

func TestEvalContextExpressionVariablesExposeSameStepDataByTypeAndName(t *testing.T) {
	ctx := NewEvalContext(EvalContextOpts{})
	ctx.SetStepData("discover", terraformaddrs.Resource{
		Mode: terraformaddrs.DataResourceMode,
		Type: "aws_caller_identity",
		Name: "current",
	}, cty.ObjectVal(map[string]cty.Value{
		"account_id": cty.StringVal("123456789012"),
	}))

	vars := ctx.expressionVariables("discover")
	got := vars["data"].GetAttr("aws_caller_identity").GetAttr("current").GetAttr("account_id")
	if got != cty.StringVal("123456789012") {
		t.Fatalf("wrong data value %#v", got)
	}
}

func TestEvalContextEvaluateExprSupportsFunctions(t *testing.T) {
	ctx := NewEvalContext(EvalContextOpts{})
	value, diags := ctx.EvaluateExpr("", mustParseExpression(t, `format("hello-%s", "world")`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if value != cty.StringVal("hello-world") {
		t.Fatalf("wrong expression value %#v", value)
	}
}

func TestEvalContextExpressionVariablesExposePluralStepsAlias(t *testing.T) {
	ctx := NewEvalContext(EvalContextOpts{})
	ctx.SetStepOutput("discover", "result", cty.StringVal("srv-123"))

	value, diags := ctx.EvaluateExpr("", mustParseExpression(t, `steps.discover.result`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if value != cty.StringVal("srv-123") {
		t.Fatalf("wrong expression value %#v", value)
	}
}

func TestEvalContextExpressionVariablesExposeListResultsUnderData(t *testing.T) {
	ctx := NewEvalContext(EvalContextOpts{})
	ctx.SetStepList("discover", terraformaddrs.Resource{
		Mode: terraformaddrs.ListResourceMode,
		Type: "aws_lambda_function",
		Name: "managed",
	}, cty.ObjectVal(map[string]cty.Value{
		"data": cty.TupleVal([]cty.Value{
			cty.ObjectVal(map[string]cty.Value{
				"identity": cty.ObjectVal(map[string]cty.Value{
					"function_name": cty.StringVal("runbook-scratchpad-ops-smoke"),
				}),
			}),
		}),
	}))

	value, diags := ctx.EvaluateExpr("discover", mustParseExpression(t, `list.aws_lambda_function.managed.data[0].identity.function_name`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if value != cty.StringVal("runbook-scratchpad-ops-smoke") {
		t.Fatalf("wrong expression value %#v", value)
	}
}

func TestEvalContextExpressionVariablesExposeWorkspaceOutputs(t *testing.T) {
	ctx := NewEvalContext(EvalContextOpts{Config: &runbookconfigs.RunbookConfig{
		WorkspaceConfig: &configs.Config{Module: &configs.Module{
			Outputs: map[string]*configs.Output{
				"workspace_region": {
					Name: "workspace_region",
					Expr: mustParseExpression(t, `"us-east-1"`),
				},
			},
		}},
	}})

	value, diags := ctx.EvaluateExpr("", mustParseExpression(t, `workspace.output.workspace_region`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if value != cty.DynamicVal {
		t.Fatalf("wrong expression value %#v", value)
	}
}

func TestEvalContextEvaluateExprForInstanceExposesRepetitionData(t *testing.T) {
	ctx := NewEvalContext(EvalContextOpts{})
	repetitionData := terraform.InstanceKeyEvalData{
		EachKey:   cty.StringVal("primary"),
		EachValue: cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("lambda-a")}),
	}

	value, diags := ctx.EvaluateExprForInstance("", terraformaddrs.StringKey("primary"), &repetitionData, mustParseExpression(t, `each.key`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if value != cty.StringVal("primary") {
		t.Fatalf("wrong each.key value %#v", value)
	}

	value, diags = ctx.EvaluateExprForInstance("", terraformaddrs.StringKey("primary"), &repetitionData, mustParseExpression(t, `each.value.name`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if value != cty.StringVal("lambda-a") {
		t.Fatalf("wrong each.value value %#v", value)
	}

	countData := terraform.InstanceKeyEvalData{CountIndex: cty.NumberIntVal(2)}
	value, diags = ctx.EvaluateExprForInstance("", terraformaddrs.IntKey(2), &countData, mustParseExpression(t, `count.index`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if !value.RawEquals(cty.NumberIntVal(2)) {
		t.Fatalf("wrong count.index value %#v", value)
	}
}
