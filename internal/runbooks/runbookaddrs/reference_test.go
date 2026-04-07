// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"reflect"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform/internal/addrs"
)

func TestParseReference_CoversAllRunbookReferenceKinds(t *testing.T) {
	tests := map[string]any{
		"var.name":                     addrs.InputVariable{Name: "name"},
		"step.deploy.result":           StepOutputValue{Step: StepInstance{Step: ConfigStep{Name: "deploy"}}, Name: "result"},
		"workspace.output.result":      WorkspaceOutputValue{Output: addrs.AbsOutputValue{Module: addrs.RootModuleInstance, OutputValue: addrs.OutputValue{Name: "result"}}},
		"workspace.action.http.notify": WorkspaceActionInstance{Action: addrs.AbsAction{Module: addrs.RootModuleInstance, Action: addrs.Action{Type: "http", Name: "notify"}}, Key: addrs.NoKey},
		"action.http.notify":           ActionInstance{Type: "http", Name: "notify"},
		"data.aws_instance.example":    DataSource{Type: "aws_instance", Name: "example"},
		"list.aws_instance.example":    List{Type: "aws_instance", Name: "example"},
		"local.name":                   addrs.LocalValue{Name: "name"},
		"count.index":                  addrs.CountAttr{Name: "index"},
		"each.key":                     addrs.ForEachAttr{Name: "key"},
	}

	for expr, want := range tests {
		t.Run(expr, func(t *testing.T) {
			traversal, diags := hclsyntax.ParseTraversalAbs([]byte(expr), "", hcl.InitialPos)
			if diags.HasErrors() {
				t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
			}

			got, refDiags := ParseReference(traversal)
			if refDiags.HasErrors() {
				t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
			}
			if !reflect.DeepEqual(got.Target, want) {
				t.Fatalf("wrong target\ngot:  %#v\nwant: %#v", got.Target, want)
			}
		})
	}
}

func TestParseStepOutputReference_StepOutput(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("step.deploy.result"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	got, refDiags := ParseStepOutputReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}
	if len(got.Remaining) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(got.Remaining))
	}

	want := StepOutputValue{
		Step: StepInstance{Step: ConfigStep{Name: "deploy"}},
		Name: "result",
	}
	if !reflect.DeepEqual(got.Target, want) {
		t.Fatalf("wrong target\ngot:  %#v\nwant: %#v", got.Target, want)
	}
}

func TestParseStepOutputReference_IndexedStepOutput(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("step.deploy[\"blue\"].result"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	got, refDiags := ParseStepOutputReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}
	if len(got.Remaining) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(got.Remaining))
	}

	want := StepOutputValue{
		Step: StepInstance{
			Step: ConfigStep{Name: "deploy"},
			Key:  addrs.StringKey("blue"),
		},
		Name: "result",
	}
	if !reflect.DeepEqual(got.Target, want) {
		t.Fatalf("wrong target\ngot:  %#v\nwant: %#v", got.Target, want)
	}
}

func TestParseWorkspaceReference_WorkspaceAction(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("workspace.action.http.notify"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	got, refDiags := ParseReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}
	if len(got.Remaining) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(got.Remaining))
	}

	want := WorkspaceActionInstance{Action: addrs.AbsAction{
		Module: addrs.RootModuleInstance,
		Action: addrs.Action{Type: "http", Name: "notify"},
	}, Key: addrs.NoKey}
	if !reflect.DeepEqual(got.Target, want) {
		t.Fatalf("wrong target\ngot:  %#v\nwant: %#v", got.Target, want)
	}
}

func TestParseWorkspaceReference_WorkspaceOutput(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("workspace.output.result"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	got, refDiags := ParseReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}
	if len(got.Remaining) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(got.Remaining))
	}

	want := WorkspaceOutputValue{Output: addrs.AbsOutputValue{
		Module:      addrs.RootModuleInstance,
		OutputValue: addrs.OutputValue{Name: "result"},
	}}
	if !reflect.DeepEqual(got.Target, want) {
		t.Fatalf("wrong target\ngot:  %#v\nwant: %#v", got.Target, want)
	}
}

func TestParseWorkspaceReference_WorkspaceModuleAction(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("workspace.module.child.action.http.notify"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	got, refDiags := ParseReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}
	if len(got.Remaining) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(got.Remaining))
	}

	want := WorkspaceActionInstance{Action: addrs.AbsAction{
		Module: addrs.RootModuleInstance.Child("child", addrs.NoKey),
		Action: addrs.Action{Type: "http", Name: "notify"},
	}, Key: addrs.NoKey}
	if !reflect.DeepEqual(got.Target, want) {
		t.Fatalf("wrong target\ngot:  %#v\nwant: %#v", got.Target, want)
	}
}

func TestParseWorkspaceReference_InvalidWorkspaceModuleOutput(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("workspace.module.child.output.result"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	_, refDiags := ParseReference(traversal)
	if !refDiags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}

func TestParseInStepReference_Action(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("action.http.notify"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	got, refDiags := ParseInStepReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}

	want := ActionInstance{Type: "http", Name: "notify"}
	if !reflect.DeepEqual(got.Target, want) {
		t.Fatalf("wrong target\ngot:  %#v\nwant: %#v", got.Target, want)
	}
	if len(got.Remaining) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(got.Remaining))
	}
}

func TestParseInStepReference_DataSource(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("data.aws_instance.example"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	got, refDiags := ParseInStepReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}

	want := DataSource{Type: "aws_instance", Name: "example"}
	if !reflect.DeepEqual(got.Target, want) {
		t.Fatalf("wrong target\ngot:  %#v\nwant: %#v", got.Target, want)
	}
	if len(got.Remaining) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(got.Remaining))
	}
}

func TestParseInStepReference_List(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("list.aws_instance.example"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	got, refDiags := ParseInStepReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}

	want := List{Type: "aws_instance", Name: "example"}
	if !reflect.DeepEqual(got.Target, want) {
		t.Fatalf("wrong target\ngot:  %#v\nwant: %#v", got.Target, want)
	}
	if len(got.Remaining) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(got.Remaining))
	}
}

func TestParseRunbookReference_Variable(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("var.name"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	got, refDiags := ParseRunbookReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}

	want := addrs.InputVariable{Name: "name"}
	if !reflect.DeepEqual(got.Target, want) {
		t.Fatalf("wrong target\ngot:  %#v\nwant: %#v", got.Target, want)
	}
	if len(got.Remaining) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(got.Remaining))
	}
}

func TestParseInStepReference_InvalidVariable(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("var.name"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	_, refDiags := ParseInStepReference(traversal)
	if !refDiags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}

func TestParseInStepReference_InvalidModule(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("module.child.output"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	_, refDiags := ParseInStepReference(traversal)
	if !refDiags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}

func TestParseInStepReference_InvalidOutputObject(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("output.result"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	_, refDiags := ParseInStepReference(traversal)
	if !refDiags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}

func TestParseInStepReference_InvalidTerraformObject(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("terraform.workspace"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	_, refDiags := ParseInStepReference(traversal)
	if !refDiags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}
