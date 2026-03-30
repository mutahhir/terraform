// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/lang"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

func ExprReferencesRunbookActionOutput(expr hcl.Expression) bool {
	if expr == nil {
		return false
	}
	for _, traversal := range expr.Variables() {
		ref, diags := addrs.ParseRefFromRunbookScope(traversal)
		if diags.HasErrors() || ref == nil {
			continue
		}
		if _, ok := ref.Subject.(addrs.RunbookAction); !ok {
			continue
		}
		if len(ref.Remaining) == 0 {
			continue
		}
		if attr, ok := ref.Remaining[0].(hcl.TraverseAttr); ok && attr.Name == "output" {
			return true
		}
	}
	return false
}

func ExprReferencesStepOutput(expr hcl.Expression) bool {
	if expr == nil {
		return false
	}
	for _, traversal := range expr.Variables() {
		ref, diags := addrs.ParseRefFromRunbookScope(traversal)
		if diags.HasErrors() || ref == nil {
			continue
		}
		if _, ok := ref.Subject.(addrs.Step); ok {
			return true
		}
		if _, ok := ref.Subject.(addrs.StepInstance); ok {
			return true
		}
	}
	return false
}

type langData struct {
	scope EvalScope
}

func (d *langData) StaticValidateReferences(refs []*addrs.Reference, self addrs.Referenceable, source addrs.Referenceable) tfdiags.Diagnostics {
	return nil
}

func (d *langData) GetCountAttr(addr addrs.CountAttr, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return attrFromObject(d.scope.Count, addr.Name, "count")
}

func (d *langData) GetForEachAttr(addr addrs.ForEachAttr, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return attrFromObject(d.scope.Each, addr.Name, "each")
}

func (d *langData) GetResource(addr addrs.Resource, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	switch addr.Mode {
	case addrs.ListResourceMode:
		return nestedAttr(d.scope.List, addr.Type, addr.Name, "list")
	default:
		return cty.DynamicVal, nil
	}
}

func (d *langData) GetLocalValue(addr addrs.LocalValue, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return attrFromObject(d.scope.Locals, addr.Name, "local")
}

func (d *langData) GetModule(addr addrs.ModuleCall, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *langData) GetPathAttr(addr addrs.PathAttr, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *langData) GetTerraformAttr(addr addrs.TerraformAttr, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *langData) GetInputVariable(addr addrs.InputVariable, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return attrFromObject(d.scope.Variables, addr.Name, "var")
}

func (d *langData) GetOutput(addr addrs.OutputValue, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *langData) GetCheckBlock(addr addrs.Check, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *langData) GetRunBlock(addr addrs.Run, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *langData) GetStep(addr addrs.Step, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return attrFromObject(d.scope.Steps, addr.Name, "steps")
}

func (d *langData) GetWorkspaceOutput(addr addrs.WorkspaceOutput, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return nestedAttr(d.scope.Workspace, "output", addr.Name, "workspace.output")
}

func (d *langData) GetRunbookAction(addr addrs.RunbookAction, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return nestedAttr(d.scope.Actions, addr.Type, addr.Name, "action")
}

func EvalExpr(expr hcl.Expression, scope EvalScope, wantType cty.Type) (cty.Value, tfdiags.Diagnostics) {
	lscope := &lang.Scope{
		Data:          &langData{scope: scope},
		ParseRef:      addrs.ParseRefFromRunbookScope,
		ExternalFuncs: scope.ExternalFuncs,
	}
	return lscope.EvalExpr(expr, wantType)
}

func attrFromObject(obj cty.Value, name, kind string) (cty.Value, tfdiags.Diagnostics) {
	if obj == cty.NilVal || !obj.Type().IsObjectType() || !obj.Type().HasAttribute(name) {
		return cty.DynamicVal, nil
	}
	return obj.GetAttr(name), nil
}

func nestedAttr(obj cty.Value, first, second, kind string) (cty.Value, tfdiags.Diagnostics) {
	if obj == cty.NilVal || !obj.Type().IsObjectType() || !obj.Type().HasAttribute(first) {
		return cty.DynamicVal, nil
	}
	inner := obj.GetAttr(first)
	if !inner.Type().IsObjectType() || !inner.Type().HasAttribute(second) {
		return cty.DynamicVal, nil
	}
	return inner.GetAttr(second), nil
}
