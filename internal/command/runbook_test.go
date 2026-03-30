// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package command

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/cli"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/runbooks/runbookeval"
	"github.com/hashicorp/terraform/internal/runbooks/runbookplanfile"
	"github.com/hashicorp/terraform/internal/states"
	"github.com/hashicorp/terraform/internal/tfdiags"
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

func TestRemapDiagnosticsToRunbookSourcesMapsLoweredMainTF(t *testing.T) {
	diag := tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Unsupported argument",
		Detail:   "bad attr",
		Subject: &hcl.Range{
			Filename: "/tmp/terraform-runbook-step-123/main.tf",
			Start:    hcl.Pos{Line: 12, Column: 5, Byte: 100},
			End:      hcl.Pos{Line: 12, Column: 16, Byte: 111},
		},
	})
	mapped := remapDiagnosticsToRunbookSources(diag, map[string][]runbookconfig.SourceMapEntry{
		"main.tf": {{
			GeneratedStartLine: 10,
			GeneratedEndLine:   14,
			OriginalRange: tfdiags.SourceRange{
				Filename: "runbook.tfrun.hcl",
				Start:    tfdiags.SourcePos{Line: 21, Column: 5, Byte: 200},
				End:      tfdiags.SourcePos{Line: 23, Column: 6, Byte: 260},
			},
		}},
	})
	if len(mapped) != 1 {
		t.Fatalf("wrong diag count: %d", len(mapped))
	}
	src := mapped[0].Source()
	if src.Subject == nil {
		t.Fatal("missing remapped subject")
	}
	if got, want := src.Subject.Filename, "runbook.tfrun.hcl"; got != want {
		t.Fatalf("wrong remapped filename: got %q want %q", got, want)
	}
	if got, want := src.Subject.Start.Line, 21; got != want {
		t.Fatalf("wrong remapped line: got %d want %d", got, want)
	}
}

func TestFormatStepExecutionPreviewIncludesPlannedOutputs(t *testing.T) {
	preview := formatStepExecutionPreview(nil, &runbookplanfile.Step{
		Name:           "inspect_workspace_lambda",
		After:          []string{"discover_workspace_context"},
		PlannedData:    []string{"data.aws_lambda_function.target"},
		PlannedActions: []string{"action.aws_lambda_invoke.smoke"},
	}, map[string]cty.Value{
		"runtime":                             cty.StringVal("python3.12"),
		"__runbook_postcondition_0_condition": cty.True,
	})
	if !strings.Contains(preview, "Step Execution Preview: inspect_workspace_lambda") {
		t.Fatalf("missing preview header: %s", preview)
	}
	if !strings.Contains(preview, `"data.aws_lambda_function.target"`) {
		t.Fatalf("missing planned data: %s", preview)
	}
	if !strings.Contains(preview, `runtime = "python3.12"`) {
		t.Fatalf("missing planned output: %s", preview)
	}
	if strings.Contains(preview, "__runbook_postcondition") {
		t.Fatalf("unexpected synthetic output in preview: %s", preview)
	}
}

func TestShowRunbookPlanSummaryWritesPlanText(t *testing.T) {
	ui := new(cli.MockUi)
	cmd := &RunbookCommand{Meta: Meta{Ui: ui}}
	cmd.showRunbookPlanSummary(&runbookplanfile.Plan{Steps: []runbookplanfile.Step{{Name: "first"}}})
	if got := ui.OutputWriter.String(); !strings.Contains(got, "Runbook Execution Plan") || !strings.Contains(got, "# Step 1: first") {
		t.Fatalf("missing plan summary output: %s", got)
	}
}

func TestRunbookTopLevelOutputsParse(t *testing.T) {
	rootDir := t.TempDir()
	cfg, diags := runbookconfig.ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "first" {
  output "done" {
    value = true
  }
}

output "all_done" {
  value = steps.first.done
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	if cfg.Outputs["all_done"] == nil {
		t.Fatal("missing top-level runbook output")
	}
}

func TestFormatPlanSummaryUsesTerraformLikeBlocks(t *testing.T) {
	known, err := ctymsgpack.Marshal(cty.StringVal("python3.12"), cty.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := ctymsgpack.Marshal(cty.UnknownVal(cty.String), cty.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}

	got := formatPlanSummary(nil, &runbookplanfile.Plan{Steps: []runbookplanfile.Step{{
		Name: "inspect_workspace_lambda",
		PlannedActionInfo: []runbookplanfile.PlannedActionInfo{{
			Address: "action.aws_lambda_invoke.smoke",
			Type:    "aws_lambda_invoke",
			Name:    "smoke",
			Config:  map[string][]byte{"function_name": known},
		}},
		PlannedQueryInfo: []runbookplanfile.PlannedQueryInfo{{
			Address: "list.aws_lambda_function.managed",
			Type:    "aws_lambda_function",
			Name:    "managed",
			Config:  map[string][]byte{"limit": mustMsgpackDynamicValue(t, cty.NumberIntVal(10))},
			Count:   2,
		}},
		PlannedDataInfo: []runbookplanfile.PlannedDataInfo{{
			Address: "data.aws_lambda_function.target",
			Type:    "aws_lambda_function",
			Name:    "target",
			Config:  map[string][]byte{"function_name": known},
		}},
		PlannedOutputs: map[string][]byte{"runtime": known, "invocation_output": unknown},
	}}})

	if !strings.Contains(got, `# Step 1: inspect_workspace_lambda will execute`) {
		t.Fatalf("missing execute header: %s", got)
	}
	if !strings.Contains(got, `inspect_workspace_lambda[reset] {`) {
		t.Fatalf("missing terraform-like step block: %s", got)
	}
	if !strings.Contains(got, `~`) {
		t.Fatalf("missing execute marker for planned step: %s", got)
	}
	if strings.Contains(got, `after = [`) {
		t.Fatalf("unexpected after metadata in plan output: %s", got)
	}
	if !strings.Contains(got, `# action.aws_lambda_invoke.smoke will invoke`) {
		t.Fatalf("missing action invocation heading: %s", got)
	}
	if !strings.Contains(got, "}\n\n      # data.aws_lambda_function.target will be read during execute") {
		t.Fatalf("missing blank line between action and data blocks: %s", got)
	}
	if !strings.Contains(got, `function_name`) || !strings.Contains(got, `"python3.12"`) {
		t.Fatalf("missing action config rendering: %s", got)
	}
	if !strings.Contains(got, `# action.aws_lambda_invoke.smoke will invoke`) {
		t.Fatalf("missing provider-backed action heading: %s", got)
	}
	if !strings.Contains(got, `# data.aws_lambda_function.target will be read during execute`) {
		t.Fatalf("missing terraform-like data heading: %s", got)
	}
	if !strings.Contains(got, `data "aws_lambda_function" "target" {`) {
		t.Fatalf("missing terraform-like data block: %s", got)
	}
	if strings.Count(got, `function_name`) < 2 {
		t.Fatalf("missing data/action config rendering: %s", got)
	}
	if !strings.Contains(got, `# list.aws_lambda_function.managed will query during execute`) {
		t.Fatalf("missing terraform-like list heading: %s", got)
	}
	if !strings.Contains(got, `list "aws_lambda_function" "managed" {`) {
		t.Fatalf("missing terraform-like list block: %s", got)
	}
	if !strings.Contains(got, `limit`) || !strings.Contains(got, `10`) {
		t.Fatalf("missing query config rendering: %s", got)
	}
	if !strings.Contains(got, `result_count`) || !strings.Contains(got, `2`) {
		t.Fatalf("missing query result count summary: %s", got)
	}
	if !strings.Contains(got, `runtime`) || !strings.Contains(got, `"python3.12"`) {
		t.Fatalf("missing concrete planned output: %s", got)
	}
	if !strings.Contains(got, `invocation_output`) || !strings.Contains(got, `(known after execute)`) {
		t.Fatalf("missing deferred planned output marker: %s", got)
	}
	if !strings.Contains(got, `1 to execute, 0 to skip.`) {
		t.Fatalf("missing plan footer: %s", got)
	}
}

func TestFormatPlanSummaryShowsSkippedStepReason(t *testing.T) {
	got := formatPlanSummary(nil, &runbookplanfile.Plan{Steps: []runbookplanfile.Step{{
		Name:         "summarize_workflow",
		KnownSkipped: true,
		SkipReason:   "step skipped by precondition",
	}}})

	if !strings.Contains(got, `# Step 1: summarize_workflow will be skipped`) {
		t.Fatalf("missing skipped header: %s", got)
	}
	if !strings.Contains(got, `reason = "step skipped by precondition"`) {
		t.Fatalf("missing skipped reason: %s", got)
	}
	if !strings.Contains(got, `0 to execute, 1 to skip.`) {
		t.Fatalf("missing skipped footer summary: %s", got)
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

func mustMsgpackDynamicValue(t *testing.T, val cty.Value) []byte {
	t.Helper()
	raw, err := ctymsgpack.Marshal(val, cty.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
