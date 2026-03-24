// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package command

import (
	"testing"

	"github.com/hashicorp/terraform/internal/runbooks/runbookeval"
	"github.com/hashicorp/terraform/internal/runbooks/runbookplanfile"
	"github.com/zclconf/go-cty/cty"
	ctymsgpack "github.com/zclconf/go-cty/cty/msgpack"
)

func TestSeedStepResultsFromPlanSingletonHydratesStepsScope(t *testing.T) {
	rolesVal := cty.TupleVal([]cty.Value{cty.StringVal("static_id")})
	raw, err := ctymsgpack.Marshal(rolesVal, cty.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}

	results := runbookeval.NewStepResults()
	seedStepResultsFromPlan(&runbookplanfile.Plan{
		Steps: []runbookplanfile.Step{{
			Name:     "discover_roles",
			BaseName: "discover_roles",
			PlannedOutputs: map[string][]byte{
				"roles": raw,
			},
		}},
	}, results)

	steps := results.ScopeValue()
	if !steps.Type().HasAttribute("discover_roles") {
		t.Fatal("missing discover_roles in steps scope")
	}
	discover := steps.GetAttr("discover_roles")
	if !discover.Type().HasAttribute("roles") {
		t.Fatal("missing roles output in discover_roles scope")
	}
	roles := discover.GetAttr("roles")
	if got, want := roles.LengthInt(), 1; got != want {
		t.Fatalf("wrong roles length: got %d want %d", got, want)
	}
	if got, want := roles.Index(cty.NumberIntVal(0)).AsString(), "static_id"; got != want {
		t.Fatalf("wrong role value: got %q want %q", got, want)
	}
}
