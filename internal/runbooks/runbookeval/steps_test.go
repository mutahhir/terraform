// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookeval

import (
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/zclconf/go-cty/cty"
)

func TestStepResultsScopeValueSingleton(t *testing.T) {
	results := NewStepResults()
	results.Set(runbookaddrs.Step{Name: "discover_roles"}.Instance(addrs.NoKey), cty.ObjectVal(map[string]cty.Value{"roles": cty.TupleVal([]cty.Value{cty.StringVal("a")})}))

	got := results.ScopeValue().GetAttr("discover_roles")
	if !got.Type().IsObjectType() || !got.Type().HasAttribute("roles") {
		t.Fatalf("expected singleton object result, got %s", got.GoString())
	}
}

func TestStepResultsScopeValueForEachProducesObjectByKey(t *testing.T) {
	results := NewStepResults()
	step := runbookaddrs.Step{Name: "inspect_role"}
	results.Set(step.Instance(addrs.StringKey("admin")), cty.ObjectVal(map[string]cty.Value{"role": cty.StringVal("admin")}))
	results.Set(step.Instance(addrs.StringKey("viewer")), cty.ObjectVal(map[string]cty.Value{"role": cty.StringVal("viewer")}))

	got := results.ScopeValue().GetAttr("inspect_role")
	if !got.Type().IsObjectType() {
		t.Fatalf("expected object result for for_each instances, got %s", got.GoString())
	}
	if got.GetAttr("admin").GetAttr("role").AsString() != "admin" {
		t.Fatal("missing admin instance result")
	}
}

func TestStepResultsScopeValueCountProducesTuple(t *testing.T) {
	results := NewStepResults()
	step := runbookaddrs.Step{Name: "do_n"}
	results.Set(step.Instance(addrs.IntKey(1)), cty.ObjectVal(map[string]cty.Value{"idx": cty.NumberIntVal(1)}))
	results.Set(step.Instance(addrs.IntKey(0)), cty.ObjectVal(map[string]cty.Value{"idx": cty.NumberIntVal(0)}))

	got := results.ScopeValue().GetAttr("do_n")
	if !got.Type().IsTupleType() {
		t.Fatalf("expected tuple result for count instances, got %s", got.GoString())
	}
	if got.Index(cty.NumberIntVal(0)).GetAttr("idx").AsBigFloat().String() != "0" {
		t.Fatal("missing count index 0 result")
	}
}
