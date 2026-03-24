// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookplanfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform/internal/states"
	"github.com/hashicorp/terraform/internal/states/statefile"
	"github.com/zclconf/go-cty/cty"
)

func TestCreateAndReadPlan(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "example.tfrunplan")
	want := &Plan{
		ConfigPath: "/tmp/example",
		Workspace:  "default",
		CreatedAt:  "2026-03-23T00:00:00Z",
		StepOrder:  []string{"first", "second"},
		Steps: []Step{
			{
				Name:           "first",
				PlannedActions: []string{"action.simple_action.target"},
				PlannedData:    []string{"data.simple_resource.current"},
			},
		},
	}
	if err := Create(planPath, CreateArgs{
		Plan: want,
		StateFile: &statefile.File{State: func() *states.State {
			s := states.NewState()
			s.RootOutputValues["enabled"] = &states.OutputValue{Value: cty.True}
			return s
		}()},
		Lowered: map[string]map[string][]byte{
			"first": {
				"main.tf": []byte("terraform {}\n"),
			},
		},
		Sources: map[string][]byte{
			"main.tfrun.hcl": []byte("runbook {}\n"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(planPath); err != nil {
		t.Fatal(err)
	}

	r, err := Open(planPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	got, err := r.ReadPlan()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != formatVersion {
		t.Fatalf("wrong format version: got %d want %d", got.Version, formatVersion)
	}
	if got.ConfigPath != want.ConfigPath || got.Workspace != want.Workspace {
		t.Fatalf("wrong plan metadata: got %#v want %#v", got, want)
	}
	if len(got.StepOrder) != 2 || got.StepOrder[0] != "first" || got.StepOrder[1] != "second" {
		t.Fatalf("wrong step order: %#v", got.StepOrder)
	}
	if len(got.Steps) != 1 || got.Steps[0].Name != "first" {
		t.Fatalf("wrong steps: %#v", got.Steps)
	}
	stateFile, err := r.ReadStateFile()
	if err != nil {
		t.Fatal(err)
	}
	if stateFile.State == nil || stateFile.State.RootOutputValues["enabled"] == nil || stateFile.State.RootOutputValues["enabled"].Value != cty.True {
		t.Fatalf("wrong embedded state outputs: %#v", stateFile.State)
	}
	lowered, err := r.ReadLoweredStepFiles("first")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(lowered["main.tf"]); got != "terraform {}\n" {
		t.Fatalf("wrong lowered step file content: %q", got)
	}
	sources, err := r.ReadSourceFiles()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(sources["main.tfrun.hcl"]); got != "runbook {}\n" {
		t.Fatalf("wrong source file content: %q", got)
	}
}
