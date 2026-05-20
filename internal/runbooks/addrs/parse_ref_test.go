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
	if output.Step.StepName != "deploy" || output.Step.InstanceKey != terraformaddrs.NoKey || output.OutputName != "result" {
		t.Fatalf("unexpected step output address: %s", output.String())
	}
}

func TestParseRefStepOutputCountInstance(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `step.deploy[0].result`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	output, ok := ref.Subject.(StepOutput)
	if !ok {
		t.Fatalf("expected StepOutput, got %T", ref.Subject)
	}
	if output.Step.StepName != "deploy" || output.Step.InstanceKey != terraformaddrs.IntKey(0) || output.OutputName != "result" {
		t.Fatalf("unexpected step output address: %s", output.String())
	}
}

func TestParseRefStepOutputForEachInstance(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `step.deploy["primary"].result`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	output, ok := ref.Subject.(StepOutput)
	if !ok {
		t.Fatalf("expected StepOutput, got %T", ref.Subject)
	}
	if output.Step.StepName != "deploy" || output.Step.InstanceKey != terraformaddrs.StringKey("primary") || output.OutputName != "result" {
		t.Fatalf("unexpected step output address: %s", output.String())
	}
}

func TestParseRefStepOutputNestedAttributeTraversal(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `step.deploy.result.foo`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	output, ok := ref.Subject.(StepOutput)
	if !ok {
		t.Fatalf("expected StepOutput, got %T", ref.Subject)
	}
	if output.Step.StepName != "deploy" || output.Step.InstanceKey != terraformaddrs.NoKey || output.OutputName != "result" {
		t.Fatalf("unexpected step output address: %s", output.String())
	}
	if len(ref.Remaining) != 1 {
		t.Fatalf("expected nested attribute traversal to remain, got %#v", ref.Remaining)
	}
	if remain, ok := ref.Remaining[0].(hcl.TraverseAttr); !ok || remain.Name != "foo" {
		t.Fatalf("unexpected remaining traversal %#v", ref.Remaining)
	}
}

func TestParseRefWholeStep(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `step.deploy`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	step, ok := ref.Subject.(Step)
	if !ok {
		t.Fatalf("expected Step, got %T", ref.Subject)
	}
	if step.Step.StepName != "deploy" || step.Step.InstanceKey != terraformaddrs.NoKey {
		t.Fatalf("unexpected step address: %s", step.String())
	}
}

func TestParseRefWholeStepCountInstance(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `step.deploy[0]`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	step, ok := ref.Subject.(Step)
	if !ok {
		t.Fatalf("expected Step, got %T", ref.Subject)
	}
	if step.Step.StepName != "deploy" || step.Step.InstanceKey != terraformaddrs.IntKey(0) {
		t.Fatalf("unexpected step address: %s", step.String())
	}
}

func TestParseRefWholeStepForEachInstance(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `step.deploy["primary"]`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	step, ok := ref.Subject.(Step)
	if !ok {
		t.Fatalf("expected Step, got %T", ref.Subject)
	}
	if step.Step.StepName != "deploy" || step.Step.InstanceKey != terraformaddrs.StringKey("primary") {
		t.Fatalf("unexpected step address: %s", step.String())
	}
}

func TestParseRefRejectsInvalidStepInstanceIndex(t *testing.T) {
	_, diags := ParseRef(mustParseTraversal(t, `step.deploy[1.5].result`))
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}

func TestParseRefRejectsPluralStepsRoot(t *testing.T) {
	_, diags := ParseRef(mustParseTraversal(t, `steps.deploy.result`))
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
}

func TestParseRefWorkspaceAction(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `workspace.action.http.notify`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	action, ok := ref.Subject.(WorkspaceAction)
	if !ok {
		t.Fatalf("expected WorkspaceAction, got %T", ref.Subject)
	}
	if action.Action.Type != "http" || action.Action.Name != "notify" {
		t.Fatalf("unexpected workspace action address: %s", action.String())
	}
}

func TestParseRefWorkspaceManagedResource(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `workspace.aws_lambda_function.main.arn`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	resource, ok := ref.Subject.(WorkspaceResource)
	if !ok {
		t.Fatalf("expected WorkspaceResource, got %T", ref.Subject)
	}
	if resource.Resource.Mode != terraformaddrs.ManagedResourceMode || resource.Resource.Type != "aws_lambda_function" || resource.Resource.Name != "main" {
		t.Fatalf("unexpected workspace resource address: %s", resource.String())
	}
	if len(ref.Remaining) != 1 {
		t.Fatalf("expected remaining traversal for attribute access, got %#v", ref.Remaining)
	}
}

func TestParseRefWorkspaceDataResource(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `workspace.data.aws_caller_identity.current.account_id`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	resource, ok := ref.Subject.(WorkspaceResource)
	if !ok {
		t.Fatalf("expected WorkspaceResource, got %T", ref.Subject)
	}
	if resource.Resource.Mode != terraformaddrs.DataResourceMode || resource.Resource.Type != "aws_caller_identity" || resource.Resource.Name != "current" {
		t.Fatalf("unexpected workspace data address: %s", resource.String())
	}
}

func TestParseRefWorkspaceModuleAction(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `workspace.module.child.action.http.notify`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	action, ok := ref.Subject.(WorkspaceAction)
	if !ok {
		t.Fatalf("expected WorkspaceAction, got %T", ref.Subject)
	}
	if len(action.Module.Calls) != 1 || action.Module.Calls[0].Name != "child" {
		t.Fatalf("unexpected workspace module path: %s", action.String())
	}
	if action.Action.Type != "http" || action.Action.Name != "notify" {
		t.Fatalf("unexpected module workspace action address: %s", action.String())
	}
}

func TestParseRefWorkspaceModuleManagedResource(t *testing.T) {
	ref, diags := ParseRef(mustParseTraversal(t, `workspace.module.child.aws_lambda_function.main.arn`))
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	resource, ok := ref.Subject.(WorkspaceResource)
	if !ok {
		t.Fatalf("expected WorkspaceResource, got %T", ref.Subject)
	}
	if len(resource.Module.Calls) != 1 || resource.Module.Calls[0].Name != "child" {
		t.Fatalf("unexpected workspace module path: %s", resource.String())
	}
	if resource.Resource.Type != "aws_lambda_function" || resource.Resource.Name != "main" {
		t.Fatalf("unexpected module workspace resource address: %s", resource.String())
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
