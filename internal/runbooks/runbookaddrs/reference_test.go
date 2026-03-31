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

func TestParseStepOutputReference_StepOutput(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("step.deploy.result"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	got, remain, refDiags := ParseStepOutputReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}
	if len(remain) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(remain))
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

	got, remain, refDiags := ParseStepOutputReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}
	if len(remain) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(remain))
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

	target, _, remain, refDiags := ParseWorkspaceReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}
	if len(remain) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(remain))
	}

	want := WorkspaceActionInstance{Action: addrs.AbsAction{
		Module: addrs.RootModuleInstance,
		Action: addrs.Action{Type: "http", Name: "notify"},
	}, Key: addrs.NoKey}
	if !reflect.DeepEqual(target, want) {
		t.Fatalf("wrong target\ngot:  %#v\nwant: %#v", target, want)
	}
}

func TestParseWorkspaceReference_WorkspaceOutput(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("workspace.output.result"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	target, _, remain, refDiags := ParseWorkspaceReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}
	if len(remain) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(remain))
	}

	want := WorkspaceOutputValue{Output: addrs.AbsOutputValue{
		Module:      addrs.RootModuleInstance,
		OutputValue: addrs.OutputValue{Name: "result"},
	}}
	if !reflect.DeepEqual(target, want) {
		t.Fatalf("wrong target\ngot:  %#v\nwant: %#v", target, want)
	}
}

func TestParseWorkspaceReference_WorkspaceModuleAction(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("workspace.module.child.action.http.notify"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	target, _, remain, refDiags := ParseWorkspaceReference(traversal)
	if refDiags.HasErrors() {
		t.Fatalf("unexpected reference diagnostics: %s", refDiags.Err())
	}
	if len(remain) != 0 {
		t.Fatalf("expected no remaining traversal, got %d steps", len(remain))
	}

	want := WorkspaceActionInstance{Action: addrs.AbsAction{
		Module: addrs.RootModuleInstance.Child("child", addrs.NoKey),
		Action: addrs.Action{Type: "http", Name: "notify"},
	}, Key: addrs.NoKey}
	if !reflect.DeepEqual(target, want) {
		t.Fatalf("wrong target\ngot:  %#v\nwant: %#v", target, want)
	}
}

func TestParseWorkspaceReference_InvalidWorkspaceModuleOutput(t *testing.T) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("workspace.module.child.output.result"), "", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}

	_, _, _, refDiags := ParseWorkspaceReference(traversal)
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
