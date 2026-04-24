package runbookplanfile

import (
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform/internal/plans"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/zclconf/go-cty/cty"
)

func TestWriteReadRoundTrip(t *testing.T) {
	td := t.TempDir()
	path := filepath.Join(td, "saved.tfrunplan")
	varValue, err := plans.NewDynamicValue(cty.StringVal("hello"), cty.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}
	outputValue, err := plans.NewDynamicValue(cty.StringVal("srv-123"), cty.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}
	stepValue, err := plans.NewDynamicValue(cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("srv-123")}), cty.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}
	plan := &Plan{
		Version:            FormatVersion,
		TerraformVersion:   "1.0.0",
		RunbookSourceDir:   "/tmp/runbook",
		Sources:            map[string][]byte{"/tmp/runbook/main.tfrun.hcl": []byte("step \"x\" {}")},
		WorkspaceSourceDir: "/tmp/workspace",
		WorkspaceSources:   map[string][]byte{"/tmp/workspace/main.tf": []byte("output \"region\" { value = \"us-east-1\" }")},
		WorkspaceStateFile: []byte("{\"version\":4}"),
		Variables:          map[string]plans.DynamicValue{"name": varValue},
		Steps: []*Step{{
			Name:           "discover",
			Index:          0,
			Status:         runbookruntime.StepStatusCompleted,
			Data:           map[string]plans.DynamicValue{"data.test_data.selected": stepValue},
			Lists:          map[string]plans.DynamicValue{"list.test_list.servers": stepValue},
			Outputs:        map[string]plans.DynamicValue{"result": outputValue},
			Actions:        map[string]*ActionState{"action.test_action.notify": {Planned: true, PlannedConfig: stepValue}},
			InstanceKey:    &InstanceKey{Kind: "string", Str: "primary"},
			RepetitionData: &RepetitionData{EachKey: "primary", EachValue: stepValue},
		}},
	}
	if err := Write(path, plan); err != nil {
		t.Fatalf("write: %s", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("read: %s", err)
	}
	if got.TerraformVersion != plan.TerraformVersion {
		t.Fatalf("wrong terraform version %q", got.TerraformVersion)
	}
	if got.RunbookSourceDir != plan.RunbookSourceDir {
		t.Fatalf("wrong source dir %q", got.RunbookSourceDir)
	}
	if got.WorkspaceSourceDir != plan.WorkspaceSourceDir {
		t.Fatalf("wrong workspace source dir %q", got.WorkspaceSourceDir)
	}
	if string(got.WorkspaceStateFile) != string(plan.WorkspaceStateFile) {
		t.Fatalf("wrong workspace state file %q", string(got.WorkspaceStateFile))
	}
	if len(got.Steps) != 1 {
		t.Fatalf("wrong step count %d", len(got.Steps))
	}
	if got.Steps[0].Actions["action.test_action.notify"] == nil {
		t.Fatal("expected saved action state")
	}
	decoded, err := got.Steps[0].Outputs["result"].Decode(cty.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.AsString() != "srv-123" {
		t.Fatalf("wrong decoded output %q", decoded.AsString())
	}
}
