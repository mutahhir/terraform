// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package command

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/runbooks/runbookeval"
	"github.com/hashicorp/terraform/internal/runbooks/runbookplanfile"
	"github.com/hashicorp/terraform/internal/states"
	"github.com/zclconf/go-cty/cty"
	ctymsgpack "github.com/zclconf/go-cty/cty/msgpack"
)

func TestSeedStepResultsFromPlanSingletonHydratesStepsScope(t *testing.T) {
	rolesVal := cty.TupleVal([]cty.Value{cty.StringVal("static_id")})
	raw, err := ctymsgpack.Marshal(rolesVal, cty.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}

	results := runbookeval.NewStepResults()
	seedStepResultsFromPlan(&runbookplanfile.Plan{
		Steps: []runbookplanfile.Step{{
			Name:     "discover_roles",
			BaseName: "discover_roles",
			PlannedOutputs: map[string][]byte{
				"roles": raw,
			},
		}},
	}, results)

	steps := results.ScopeValue()
	if !steps.Type().HasAttribute("discover_roles") {
		t.Fatal("missing discover_roles in steps scope")
	}
	discover := steps.GetAttr("discover_roles")
	if !discover.Type().HasAttribute("roles") {
		t.Fatal("missing roles output in discover_roles scope")
	}
	roles := discover.GetAttr("roles")
	if got, want := roles.LengthInt(), 1; got != want {
		t.Fatalf("wrong roles length: got %d want %d", got, want)
	}
	if got, want := roles.Index(cty.NumberIntVal(0)).AsString(), "static_id"; got != want {
		t.Fatalf("wrong role value: got %q want %q", got, want)
	}
}

func TestMergeStepOutputsPrefersKnownStateOverUnknownPlannedValues(t *testing.T) {
	step := &runbookconfig.Step{
		Outputs: map[string]*runbookconfig.Output{
			"region_name": {},
		},
	}
	state := states.NewState()
	state.SetOutputValue(addrs.OutputValue{Name: "region_name"}.Absolute(addrs.RootModuleInstance), cty.StringVal("us-east-1"), false)

	planned := cty.ObjectVal(map[string]cty.Value{
		"region_name": cty.UnknownVal(cty.String),
	})

	got := mergeStepOutputs(stepOutputsFromState(state, step), planned)
	if !got.Type().HasAttribute("region_name") {
		t.Fatal("missing region_name output")
	}
	if gotVal := got.GetAttr("region_name"); !gotVal.RawEquals(cty.StringVal("us-east-1")) {
		t.Fatalf("wrong merged output: %#v", gotVal)
	}
}

func TestLoadRunbookVariableValuesUsesCollectedInputs(t *testing.T) {
	t.Setenv("TF_VAR_aws_profile", "scratch-profile")

	cmd := &RunbookCommand{}
	scope, inputs, diags := cmd.loadRunbookVariableValues(&runbookconfig.Config{
		Variables: map[string]*runbookconfig.Variable{
			"aws_profile": {
				Name: "aws_profile",
			},
			"aws_region": {
				Name:    "aws_region",
				Default: mustParseExpr(t, `"us-east-1"`),
			},
		},
	})
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	if got := scope.GetAttr("aws_profile"); !got.RawEquals(cty.StringVal("scratch-profile")) {
		t.Fatalf("wrong aws_profile in scope: %#v", got)
	}
	if got := inputs["aws_profile"].Value; !got.RawEquals(cty.StringVal("scratch-profile")) {
		t.Fatalf("wrong aws_profile input value: %#v", got)
	}
	if got := scope.GetAttr("aws_region"); !got.RawEquals(cty.StringVal("us-east-1")) {
		t.Fatalf("wrong aws_region in scope: %#v", got)
	}
}

func TestMergedWorkspaceScopePrefersWorkspaceStateOutputs(t *testing.T) {
	base := cty.ObjectVal(map[string]cty.Value{
		"output": cty.ObjectVal(map[string]cty.Value{
			"smoke_lambda_name": cty.StringVal("workspace-name"),
		}),
	})
	stepState := states.NewState()
	stepState.SetOutputValue(addrs.OutputValue{Name: "smoke_lambda_name"}.Absolute(addrs.RootModuleInstance), cty.StringVal("step-name"), false)

	got := mergedWorkspaceScope(base, stepState)
	if gotVal := got.GetAttr("output").GetAttr("smoke_lambda_name"); !gotVal.RawEquals(cty.StringVal("step-name")) {
		t.Fatalf("wrong merged workspace output: %#v", gotVal)
	}
}

func mustParseExpr(t *testing.T, src string) hcl.Expression {
	t.Helper()
	expr, diags := hclsyntax.ParseExpression([]byte(src), "test.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	return expr
}
