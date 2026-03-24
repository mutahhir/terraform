// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookplan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/zclconf/go-cty/cty"
)

func TestBuildExpandsForEachFromQueryDerivedOutputs(t *testing.T) {
	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "discover_roles" {
  list "simple_resource" "roles" {
    provider = simple
  }

  output "roles" {
    value = [for role in list.simple_resource.roles.data : role.identity.id]
  }
}

step "inspect_role" {
  for_each = steps.discover_roles.roles

  execute {}
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, diags := runbookconfig.LoadConfigDir(configPath)
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}

	build, diags, err := Build(cfg, configPath, "default", []string{"discover_roles", "inspect_role"}, map[string][]string{"inspect_role": {"discover_roles"}}, cty.EmptyObjectVal, cty.EmptyObjectVal, func(stepName string, scope runbookconfig.EvalScope) (StepPlanResult, error) {
		switch stepName {
		case "discover_roles":
			return StepPlanResult{
				Outputs: cty.EmptyObjectVal,
				Queries: []runbookconfig.PlannedQuery{{
					Address: "list.simple_resource.roles",
					Count:   1,
					Data: cty.TupleVal([]cty.Value{cty.ObjectVal(map[string]cty.Value{
						"identity": cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("i-1")}),
					})}),
				}},
				OutputNames: []string{"roles"},
			}, nil
		case "inspect_role":
			return StepPlanResult{OutputNames: []string{"ready"}}, nil
		default:
			return StepPlanResult{}, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	if got, want := len(build.Manifest.StepOrder), 2; got != want {
		t.Fatalf("wrong step count: got %d want %d (%v)", got, want, build.Manifest.StepOrder)
	}
	if got, want := build.Manifest.StepOrder[1], "inspect_role[i-1]"; got != want {
		t.Fatalf("wrong expanded step instance: got %q want %q", got, want)
	}
}
