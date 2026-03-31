// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/gocty"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/instances"
	"github.com/hashicorp/terraform/internal/lang"
	"github.com/hashicorp/terraform/internal/lang/marks"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type repetitionEvaluator struct {
	ctx *RunbookContext
}

func newRepetitionEvaluator(ctx *RunbookContext) *repetitionEvaluator {
	return &repetitionEvaluator{ctx: ctx}
}

func (e *repetitionEvaluator) stepInstances(step *Step) (map[addrs.InstanceKey]*StepInstance, bool, tfdiags.Diagnostics) {
	baseAddr := runbookaddrs.StepInstance{Step: step.Addr()}
	if step.config.Count == nil && step.config.ForEach == nil {
		return map[addrs.InstanceKey]*StepInstance{
			addrs.NoKey: {
				step: step,
				addr: baseAddr,
			},
		}, false, nil
	}

	if step.config.Count != nil {
		count, unknown, diags := e.evaluateCount(step)
		if diags.HasErrors() || unknown {
			return nil, unknown, diags
		}
		ret := make(map[addrs.InstanceKey]*StepInstance, count)
		for i := range count {
			key := addrs.IntKey(i)
			ret[key] = &StepInstance{
				step: step,
				addr: runbookaddrs.StepInstance{Step: baseAddr.Step, Key: key},
				repetition: instances.RepetitionData{
					CountIndex: cty.NumberIntVal(int64(i)),
				},
			}
		}
		return ret, false, diags
	}

	forEachVal, unknown, diags := e.evaluateForEach(step)
	if diags.HasErrors() || unknown {
		return nil, unknown, diags
	}

	ret := make(map[addrs.InstanceKey]*StepInstance)
	ty := forEachVal.Type()
	switch {
	case ty.IsMapType() || ty.IsObjectType():
		elems := forEachVal.AsValueMap()
		keys := make([]string, 0, len(elems))
		for key := range elems {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, keyStr := range keys {
			key := addrs.StringKey(keyStr)
			ret[key] = &StepInstance{
				step: step,
				addr: runbookaddrs.StepInstance{Step: baseAddr.Step, Key: key},
				repetition: instances.RepetitionData{
					EachKey:   cty.StringVal(keyStr),
					EachValue: elems[keyStr],
				},
			}
		}
	case ty.IsSetType() && ty.ElementType().Equals(cty.String):
		elems := forEachVal.AsValueSlice()
		sort.Slice(elems, func(i, j int) bool {
			return elems[i].AsString() < elems[j].AsString()
		})
		for _, elem := range elems {
			keyStr := elem.AsString()
			key := addrs.StringKey(keyStr)
			ret[key] = &StepInstance{
				step: step,
				addr: runbookaddrs.StepInstance{Step: baseAddr.Step, Key: key},
				repetition: instances.RepetitionData{
					EachKey:   cty.StringVal(keyStr),
					EachValue: elem,
				},
			}
		}
	default:
		panic(fmt.Sprintf("invalid for_each value %#v", forEachVal))
	}

	return ret, false, diags
}

func (e *repetitionEvaluator) evaluateCount(step *Step) (int, bool, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	val, moreDiags := e.evalExpr(step, step.config.Count)
	diags = diags.Append(moreDiags)
	if diags.HasErrors() {
		return -1, false, diags
	}

	val, _ = val.Unmark()
	if val.IsNull() {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid count argument",
			Detail:   `The given "count" argument value is null. An integer is required.`,
			Subject:  step.config.Count.Range().Ptr(),
		})
		return -1, false, diags
	}
	if !val.IsKnown() {
		return -1, true, diags
	}
	var count int
	if err := gocty.FromCtyValue(val, &count); err != nil {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid count argument",
			Detail:   fmt.Sprintf(`The given "count" argument value is unsuitable: %s.`, err),
			Subject:  step.config.Count.Range().Ptr(),
		})
		return -1, false, diags
	}
	if count < 0 {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid count argument",
			Detail:   `The given "count" argument value is unsuitable: must be greater than or equal to zero.`,
			Subject:  step.config.Count.Range().Ptr(),
		})
		return -1, false, diags
	}
	return count, false, diags
}

func (e *repetitionEvaluator) evaluateForEach(step *Step) (cty.Value, bool, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	val, moreDiags := e.evalExpr(step, step.config.ForEach)
	diags = diags.Append(moreDiags)
	if diags.HasErrors() {
		return cty.DynamicVal, false, diags
	}

	const summary = "Invalid for_each value"
	detail := `The for_each expression must produce either a map of any type or a set of strings. The keys of the map or the set elements will serve as unique identifiers for multiple instances of this step.`

	if marks.Has(val, marks.Sensitive) {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  summary,
			Detail:   `Sensitive values, or values derived from sensitive values, cannot be used as for_each arguments. If used, the sensitive value could be exposed as a step instance key.`,
			Subject:  step.config.ForEach.Range().Ptr(),
		})
		return cty.DynamicVal, false, diags
	}

	if val.IsNull() {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  summary,
			Detail:   detail + ` The for_each expression produced a null value.`,
			Subject:  step.config.ForEach.Range().Ptr(),
		})
		return cty.DynamicVal, false, diags
	}

	ty := val.Type()
	switch {
	case ty.IsObjectType() || ty.IsMapType():
		if !val.IsKnown() {
			return val, true, diags
		}
		return val, false, diags
	case ty.IsSetType():
		if !val.IsWhollyKnown() {
			return val, true, diags
		}
		if !ty.ElementType().Equals(cty.String) {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  summary,
				Detail:   fmt.Sprintf(`%s "for_each" supports maps and sets of strings, but you have provided a set containing type %s.`, detail, ty.ElementType().FriendlyName()),
				Subject:  step.config.ForEach.Range().Ptr(),
			})
			return cty.DynamicVal, false, diags
		}
		for i, elem := range val.AsValueSlice() {
			if elem.IsNull() {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  summary,
					Detail:   fmt.Sprintf(`%s The for_each value must not contain null elements, but the element at index %d was null.`, detail, i),
					Subject:  step.config.ForEach.Range().Ptr(),
				})
			}
		}
		return val, false, diags
	case !val.IsWhollyKnown() && ty.HasDynamicTypes():
		return val, true, diags
	default:
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  summary,
			Detail:   detail,
			Subject:  step.config.ForEach.Range().Ptr(),
		})
		return cty.DynamicVal, false, diags
	}
}

func (e *repetitionEvaluator) evalExpr(step *Step, expr hcl.Expression) (cty.Value, tfdiags.Diagnostics) {
	if expr == nil {
		return cty.NilVal, nil
	}
	scope := &lang.Scope{
		Data:     &runbookRepetitionData{ctx: e.ctx, step: step},
		ParseRef: addrs.ParseRef,
		PureOnly: true,
	}
	return scope.EvalExpr(expr, cty.DynamicPseudoType)
}

type runbookRepetitionData struct {
	ctx  *RunbookContext
	step *Step
}

func (d *runbookRepetitionData) StaticValidateReferences(refs []*addrs.Reference, self addrs.Referenceable, source addrs.Referenceable) tfdiags.Diagnostics {
	return nil
}

func (d *runbookRepetitionData) GetCountAttr(addrs.CountAttr, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.UnknownVal(cty.Number), nil
}

func (d *runbookRepetitionData) GetForEachAttr(addrs.ForEachAttr, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *runbookRepetitionData) GetResource(addr addrs.Resource, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if addr.Mode == addrs.DataResourceMode || addr.Mode == addrs.ListResourceMode {
		return cty.DynamicVal, diags
	}
	return cty.DynamicVal, diags.Append(&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Invalid repetition reference",
		Detail:   fmt.Sprintf("The reference %q is not available while evaluating step repetition.", addr.String()),
		Subject:  rng.ToHCL().Ptr(),
	})
}

func (d *runbookRepetitionData) GetLocalValue(addr addrs.LocalValue, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	if d.ctx == nil || d.step == nil {
		return cty.DynamicVal, nil
	}
	local := d.ctx.StepLocal(d.step.Name(), addr.Name)
	if local == nil {
		return cty.DynamicVal, nil
	}
	return d.evalStepLocal(local)
}

func (d *runbookRepetitionData) GetModule(addrs.ModuleCall, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *runbookRepetitionData) GetPathAttr(addrs.PathAttr, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *runbookRepetitionData) GetTerraformAttr(addrs.TerraformAttr, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *runbookRepetitionData) GetInputVariable(addr addrs.InputVariable, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	if d.ctx == nil {
		return cty.DynamicVal, nil
	}
	if val, ok := d.ctx.planTimeInputs.Variables[addr.Name]; ok {
		return val, nil
	}
	variable := d.ctx.Variable(addr.Name)
	if variable == nil {
		return cty.DynamicVal, nil
	}
	if variable.Default == cty.NilVal {
		if variable.ConstraintType != cty.NilType {
			return cty.UnknownVal(variable.ConstraintType), nil
		}
		if variable.Type != cty.NilType {
			return cty.UnknownVal(variable.Type), nil
		}
		return cty.DynamicVal, nil
	}
	return variable.Default, nil
}

func (d *runbookRepetitionData) GetOutput(addr addrs.OutputValue, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	if d.ctx == nil || d.ctx.config == nil || d.ctx.config.WorkspaceConfig == nil {
		return cty.DynamicVal, nil
	}
	if val, ok := d.ctx.planTimeInputs.WorkspaceOutputs[addr.Name]; ok {
		return val, nil
	}
	output := d.ctx.config.WorkspaceConfig.Module.Outputs[addr.Name]
	if output == nil {
		return cty.DynamicVal, nil
	}
	if output.Expr == nil {
		return cty.DynamicVal, nil
	}
	val, hclDiags := output.Expr.Value(nil)
	var diags tfdiags.Diagnostics
	diags = diags.Append(hclDiags)
	return val, diags
}

func (d *runbookRepetitionData) GetCheckBlock(addr addrs.Check, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *runbookRepetitionData) GetRunBlock(addr addrs.Run, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *runbookRepetitionData) evalStepLocal(local *configs.Local) (cty.Value, tfdiags.Diagnostics) {
	if local == nil || local.Expr == nil {
		return cty.DynamicVal, nil
	}
	scope := &lang.Scope{
		Data:     d,
		ParseRef: addrs.ParseRef,
		PureOnly: true,
	}
	return scope.EvalExpr(local.Expr, cty.DynamicPseudoType)
}

func unknownForEachRepetitionData(step *Step) instances.RepetitionData {
	if step == nil || step.config == nil || step.config.ForEach == nil {
		return instances.TotallyUnknownRepetitionData
	}
	eval := newRepetitionEvaluator(step.context)
	val, _, diags := eval.evaluateForEach(step)
	if diags.HasErrors() {
		return instances.TotallyUnknownRepetitionData
	}
	return instances.UnknownForEachRepetitionData(val.Type())
}
