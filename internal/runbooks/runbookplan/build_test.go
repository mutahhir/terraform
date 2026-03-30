// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookplan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/plans"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/runbooks/runbookplanfile"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

func TestBuildPreservesActionOutputBackedOutputsForDownstreamScopes(t *testing.T) {
	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "invoke" {
  for_each = {
    primary = "primary"
    shadow  = "shadow"
  }

  output "invoke_target" {
    value = each.key
  }

  output "invocation_output" {
    value = trimspace(action.simple_action.smoke.output)
  }
}

step "summary" {
  output "primary_output" {
    value = one([for step in values(steps.invoke) : step.invocation_output if step.invoke_target == "primary"])
  }
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, diags := runbookconfig.LoadConfigDir(configPath)
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}

	_, diags, err = Build(cfg, configPath, "default", []string{"invoke", "summary"}, map[string][]string{"summary": {"invoke"}}, cty.EmptyObjectVal, cty.EmptyObjectVal, func(stepName string, scope runbookconfig.EvalScope) (StepPlanResult, error) {
		switch stepName {
		case "invoke":
			return StepPlanResult{
				Outputs: cty.ObjectVal(map[string]cty.Value{
					"invoke_target": scope.Each.GetAttr("key"),
				}),
				OutputNames: []string{"invoke_target", "invocation_output"},
			}, nil
		case "summary":
			step := cfg.Files[filepath.Join(configPath, "main.tfrun.hcl")].Steps["summary"]
			val, evalDiags := runbookconfig.EvalExpr(step.Outputs["primary_output"].Value, scope, cty.DynamicPseudoType)
			if evalDiags.HasErrors() {
				return StepPlanResult{}, evalDiags.Err()
			}
			if got, want := val.Type(), cty.String; !got.Equals(want) {
				return StepPlanResult{}, fmt.Errorf("wrong summary output type: got %s want %s", got.FriendlyName(), want.FriendlyName())
			}
			return StepPlanResult{OutputNames: []string{"primary_output"}}, nil
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
}

func TestBuildPreservesUnknownDerivedOutputsFromForEachSteps(t *testing.T) {
	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "invoke" {
  for_each = {
    primary = "primary"
    shadow  = "shadow"
  }

  output "invoke_target" {
    value = each.key
  }

  output "invocation_output" {
    value = trimspace(action.simple_action.smoke.output)
  }
}

step "summary" {
  output "primary_output" {
    value = one([for step in values(steps.invoke) : step.invocation_output if step.invoke_target == "primary"])
  }
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, diags := runbookconfig.LoadConfigDir(configPath)
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}

	plan, diags, err := Build(cfg, configPath, "default", []string{"invoke", "summary"}, map[string][]string{"summary": {"invoke"}}, cty.EmptyObjectVal, cty.EmptyObjectVal, func(stepName string, scope runbookconfig.EvalScope) (StepPlanResult, error) {
		switch stepName {
		case "invoke":
			return StepPlanResult{
				Outputs: cty.ObjectVal(map[string]cty.Value{
					"invoke_target": scope.Each.GetAttr("key"),
				}),
				OutputNames: []string{"invoke_target", "invocation_output"},
			}, nil
		case "summary":
			step := cfg.Files[filepath.Join(configPath, "main.tfrun.hcl")].Steps["summary"]
			val, evalDiags := runbookconfig.EvalExpr(step.Outputs["primary_output"].Value, scope, cty.DynamicPseudoType)
			if evalDiags.HasErrors() {
				return StepPlanResult{}, evalDiags.Err()
			}
			return StepPlanResult{Outputs: cty.ObjectVal(map[string]cty.Value{"primary_output": val}), OutputNames: []string{"primary_output"}}, nil
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

	var summary *runbookplanfile.Step
	for i := range plan.Manifest.Steps {
		if plan.Manifest.Steps[i].Name == "summary" {
			summary = &plan.Manifest.Steps[i]
			break
		}
	}
	if summary == nil {
		t.Fatal("missing summary step")
	}
	raw, ok := summary.PlannedOutputs["primary_output"]
	if !ok {
		t.Fatal("missing derived summary output")
	}
	val, err := plans.DynamicValue(raw).Decode(cty.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}
	if val.IsKnown() || val.IsNull() {
		t.Fatalf("expected unknown derived output, got %s", tfdiags.CompactValueStr(val))
	}
}

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
  for_each = toset(steps.discover_roles.roles)

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
	wantAddr := runbookaddrs.Step{Name: "inspect_role"}.Instance(addrs.StringKey("i-1")).String()
	if got, want := build.Manifest.StepOrder[1], wantAddr; got != want {
		t.Fatalf("wrong expanded step instance: got %q want %q", got, want)
	}
	step := build.Manifest.Steps[1]
	if got, want := step.ForEachKey, "i-1"; got != want {
		t.Fatalf("wrong instance key: got %q want %q", got, want)
	}
	if len(step.ForEachValue) == 0 {
		t.Fatal("expected encoded for_each value")
	}
	if len(build.Manifest.Steps[0].PlannedOutputs) == 0 {
		t.Fatal("expected planned outputs to be persisted for discover_roles")
	}
}

func TestBuildRejectsListForEachLikeTerraform(t *testing.T) {
	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "inspect_role" {
  for_each = ["a", "b"]

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

	_, diags, err = Build(cfg, configPath, "default", []string{"inspect_role"}, nil, cty.EmptyObjectVal, cty.EmptyObjectVal, func(stepName string, scope runbookconfig.EvalScope) (StepPlanResult, error) {
		return StepPlanResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !diags.HasErrors() && len(buildManifestNames(t, cfg, configPath)) != 0 {
		t.Fatal("expected invalid for_each diagnostic")
	}
	if !diags.HasErrors() {
		t.Fatal("expected invalid for_each diagnostic")
	}
	if got, want := diags.Err().Error(), `must be a map, or set of strings, and you have provided a value of type tuple`; !strings.Contains(got, want) {
		t.Fatalf("wrong diagnostic: got %q want substring %q", got, want)
	}
}

func buildManifestNames(t *testing.T, cfg *runbookconfig.Config, configPath string) []string {
	t.Helper()
	build, diags, err := Build(cfg, configPath, "default", []string{"inspect_role"}, nil, cty.EmptyObjectVal, cty.EmptyObjectVal, func(stepName string, scope runbookconfig.EvalScope) (StepPlanResult, error) {
		return StepPlanResult{}, nil
	})
	if err != nil || diags.HasErrors() || build == nil || build.Manifest == nil {
		return nil
	}
	return build.Manifest.StepOrder
}
