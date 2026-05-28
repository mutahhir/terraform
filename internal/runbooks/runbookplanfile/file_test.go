package runbookplanfile

import (
	"bytes"
	"os"
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
		TerraformVersion:   "1.14.0-dev",
		RunbookSourceDir:   "/tmp/runbook",
		Sources:            map[string][]byte{"main.tfrun.hcl": []byte("step \"x\" {}")},
		WorkspaceSourceDir: "/tmp/workspace",
		WorkspaceSources:   map[string][]byte{"main.tf": []byte("output \"region\" { value = \"us-east-1\" }")},
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

func TestReadRejectsTruncatedZip(t *testing.T) {
	td := t.TempDir()
	path := filepath.Join(td, "good.tfrunplan")

	// Write a valid plan first
	plan := &Plan{
		Version:          FormatVersion,
		TerraformVersion: "1.14.0-dev",
		Sources:          map[string][]byte{},
		WorkspaceSources: map[string][]byte{},
	}
	if err := Write(path, plan); err != nil {
		t.Fatal(err)
	}

	// Truncate it
	data, _ := os.ReadFile(path)
	truncated := data[:len(data)/2]
	corruptPath := filepath.Join(td, "corrupt.tfrunplan")
	os.WriteFile(corruptPath, truncated, 0644)

	_, err := Read(corruptPath)
	if err == nil {
		t.Fatal("expected error for truncated zip")
	}
}

func TestReadRejectsWrongVersion(t *testing.T) {
	td := t.TempDir()
	path := filepath.Join(td, "badversion.tfrunplan")

	plan := &Plan{
		Version:          99,
		TerraformVersion: "1.14.0-dev",
		Sources:          map[string][]byte{},
		WorkspaceSources: map[string][]byte{},
	}
	if err := Write(path, plan); err != nil {
		t.Fatal(err)
	}

	_, err := Read(path)
	if err == nil {
		t.Fatal("expected error for wrong version")
	}
}

func TestReadRejectsEmptyFile(t *testing.T) {
	td := t.TempDir()
	path := filepath.Join(td, "empty.tfrunplan")
	os.WriteFile(path, []byte{}, 0644)

	_, err := Read(path)
	if err == nil {
		t.Fatal("expected error for empty file")
	}
}

func TestWriteCompressesLargeState(t *testing.T) {
	td := t.TempDir()
	path := filepath.Join(td, "large.tfrunplan")

	// Create a large state (repetitive JSON compresses well)
	largeState := bytes.Repeat([]byte(`{"resources":[{"type":"aws_instance","name":"web","values":{"id":"i-12345","ami":"ami-abcde"}}]}`), 1000)

	plan := &Plan{
		Version:            FormatVersion,
		TerraformVersion:   "1.14.0-dev",
		Sources:            map[string][]byte{},
		WorkspaceSources:   map[string][]byte{},
		WorkspaceStateFile: largeState,
	}
	if err := Write(path, plan); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// The zip with Deflate should be significantly smaller than the raw state
	rawSize := len(largeState)
	zipSize := info.Size()
	ratio := float64(zipSize) / float64(rawSize)
	if ratio > 0.3 {
		t.Fatalf("expected significant compression, but zip is %.0f%% of raw size (%d vs %d bytes)", ratio*100, zipSize, rawSize)
	}

	// Verify round-trip
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.WorkspaceStateFile, largeState) {
		t.Fatal("state didn't round-trip correctly")
	}
}
