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
