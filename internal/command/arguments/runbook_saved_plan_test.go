package arguments

import "testing"

func TestParseRunbookPlanOut(t *testing.T) {
	args, diags := ParseRunbookPlan([]string{"-out=saved.tfrunplan"})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if args.OutPath != "saved.tfrunplan" {
		t.Fatalf("wrong out path %q", args.OutPath)
	}
}

func TestParseRunbookExecutePlanPath(t *testing.T) {
	args, diags := ParseRunbookExecute([]string{"saved.tfrunplan"})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if args.PlanPath != "saved.tfrunplan" {
		t.Fatalf("wrong plan path %q", args.PlanPath)
	}
}

func TestParseRunbookExecuteJSONAllowsSavedPlanWithoutAutoApprove(t *testing.T) {
	args, diags := ParseRunbookExecute([]string{"-json", "saved.tfrunplan"})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if args.PlanPath != "saved.tfrunplan" {
		t.Fatalf("wrong plan path %q", args.PlanPath)
	}
}

func TestParseRunbookExecuteRejectsVarsWithSavedPlan(t *testing.T) {
	_, diags := ParseRunbookExecute([]string{"-var", "name=value", "saved.tfrunplan"})
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics")
	}
}

func TestParseRunbookShowPlanPath(t *testing.T) {
	args, diags := ParseRunbookShow([]string{"saved.tfrunplan"})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if args.PlanPath != "saved.tfrunplan" {
		t.Fatalf("wrong plan path %q", args.PlanPath)
	}
}

func TestParseRunbookShowRequiresPath(t *testing.T) {
	_, diags := ParseRunbookShow(nil)
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics")
	}
}

func TestParseRunbookShowJSON(t *testing.T) {
	args, diags := ParseRunbookShow([]string{"-json", "saved.tfrunplan"})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if args.ViewType != ViewJSON {
		t.Fatalf("wrong view type %v", args.ViewType)
	}
}
