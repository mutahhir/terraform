// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookplan

import (
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/lang"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/runbooks/runbookeval"
	"github.com/hashicorp/terraform/internal/runbooks/runbookplanfile"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
	ctymsgpack "github.com/zclconf/go-cty/cty/msgpack"
)

type StepPlanResult struct {
	Outputs        cty.Value
	Queries        []runbookconfig.PlannedQuery
	KnownSkipped   bool
	SkipReason     string
	PlannedActions []string
	PlannedQueries []string
	PlannedData    []string
	OutputNames    []string
	LoweredFiles   map[string][]byte
}

type StepPlanner func(stepName string, scope runbookconfig.EvalScope) (StepPlanResult, error)

type BuildResult struct {
	Manifest    *runbookplanfile.Plan
	Lowered     map[string]map[string][]byte
	StepResults *runbookeval.StepResults
}

func Build(cfg *runbookconfig.Config, configPath, workspace string, stepOrder []string, deps map[string][]string, varScope cty.Value, workspaceScope cty.Value, planner StepPlanner) (*BuildResult, tfdiags.Diagnostics, error) {
	var diags tfdiags.Diagnostics
	manifest := &runbookplanfile.Plan{ConfigPath: configPath, Workspace: workspace}
	lowered := make(map[string]map[string][]byte)
	stepResults := runbookeval.NewStepResults()
	rawSteps := stepsByName(cfg)

	for _, stepName := range stepOrder {
		step := rawSteps[stepName]
		if step == nil {
			continue
		}
		result, err := planner(stepName, runbookconfig.EvalScope{
			Variables: varScope,
			Steps:     stepResults.ScopeValue(),
			Workspace: workspaceScope,
		})
		if err != nil {
			return nil, diags, err
		}
		result.Outputs = mergeOutputs(result.Outputs, StepOutputsFromQueries(step, result.Queries, varScope, stepResults.ScopeValue(), workspaceScope))
		expanded, moreDiags := expandStepInstances(step, stepName, result.Outputs, stepResults.ScopeValue(), workspaceScope, varScope)
		diags = diags.Append(moreDiags)
		if moreDiags.HasErrors() {
			return nil, diags, nil
		}
		if step.Count == nil && step.ForEach == nil && len(expanded) == 0 {
			expanded = []runbookplanfile.Step{singletonStepManifest(step, stepName, deps[stepName], result)}
		}
		for _, expandedStep := range expanded {
			expandedStep.After = append([]string(nil), deps[stepName]...)
			expandedStep.KnownSkipped = result.KnownSkipped
			expandedStep.SkipReason = result.SkipReason
			expandedStep.PlannedActions = append([]string(nil), result.PlannedActions...)
			expandedStep.PlannedQueries = append([]string(nil), result.PlannedQueries...)
			expandedStep.PlannedData = append([]string(nil), result.PlannedData...)
			expandedStep.Outputs = append([]string(nil), result.OutputNames...)
			manifest.StepOrder = append(manifest.StepOrder, expandedStep.Name)
			manifest.Steps = append(manifest.Steps, expandedStep)
			stepResults.Set(stepInstanceAddr(expandedStep), decodePlannedOutputsMap(expandedStep.PlannedOutputs))
			if len(result.LoweredFiles) > 0 {
				loweredBundle := result.LoweredFiles
				if expandedStep.ForEachExpression != "" || expandedStep.CountExpression != "" {
					var eachVal cty.Value
					var countVal cty.Value
					if expandedStep.ForEachExpression != "" {
						eachVal = eachScopeForExpandedStep(&expandedStep)
					}
					if expandedStep.CountIndex != nil {
						countVal = countScopeForExpandedStep(&expandedStep)
					}
					if loweredInstance, lowerDiags := runbookconfig.LowerStepInstance(cfg, step, eachVal, countVal); lowerDiags.HasErrors() {
						return nil, diags.Append(lowerDiags), nil
					} else if loweredInstance != nil {
						loweredBundle = loweredInstance.Files
					}
				}
				lowered[expandedStep.Name] = loweredBundle
			}
		}
	}

	return &BuildResult{Manifest: manifest, Lowered: lowered, StepResults: stepResults}, diags, nil
}

func encodePlannedOutputs(v cty.Value) map[string][]byte {
	if v == cty.NilVal || !v.Type().IsObjectType() {
		return nil
	}
	ret := make(map[string][]byte)
	for name, val := range v.AsValueMap() {
		encoded, err := ctymsgpack.Marshal(val, cty.DynamicPseudoType)
		if err != nil {
			continue
		}
		ret[name] = encoded
	}
	if len(ret) == 0 {
		return nil
	}
	return ret
}

func stepsByName(cfg *runbookconfig.Config) map[string]*runbookconfig.Step {
	ret := make(map[string]*runbookconfig.Step)
	if cfg == nil {
		return ret
	}
	for _, file := range cfg.Files {
		for name, step := range file.Steps {
			ret[name] = step
		}
	}
	return ret
}

func expandStepInstances(step *runbookconfig.Step, stepName string, outputs cty.Value, plannedSteps cty.Value, workspaceScope cty.Value, varScope cty.Value) ([]runbookplanfile.Step, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if step == nil {
		return nil, diags
	}
	if step.Count == nil && step.ForEach == nil {
		return nil, diags
	}
	if step.ForEach != nil {
		val, evalDiags := runbookconfig.EvalExpr(step.ForEach, runbookconfig.EvalScope{Variables: varScope, Steps: plannedSteps, Workspace: workspaceScope}, cty.DynamicPseudoType)
		diags = diags.Append(evalDiags)
		if evalDiags.HasErrors() || val == cty.NilVal || !val.IsKnown() || val.IsNull() {
			return nil, diags
		}
		if !(val.Type().IsMapType() || val.Type().IsObjectType() || val.Type().IsSetType()) {
			return nil, diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid for_each argument",
				Detail:   fmt.Sprintf(`The given "for_each" argument value is unsuitable: the "for_each" argument must be a map, or set of strings, and you have provided a value of type %s.`, val.Type().FriendlyName()),
				Subject:  step.ForEach.Range().Ptr(),
			})
		}
		if val.Type().IsSetType() && val.Type().ElementType() != cty.String {
			return nil, diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid for_each set argument",
				Detail:   fmt.Sprintf(`The given "for_each" argument value is unsuitable: "for_each" supports maps and sets of strings, but you have provided a set containing type %s.`, val.Type().ElementType().FriendlyName()),
				Subject:  step.ForEach.Range().Ptr(),
			})
		}
		return expandForEachInstances(step, stepName, val, outputs), diags
	}
	count, countDiags := evaluateCountExpression(step.Count, runbookconfig.EvalScope{Variables: varScope, Steps: plannedSteps, Workspace: workspaceScope})
	diags = diags.Append(countDiags)
	if countDiags.HasErrors() {
		return nil, diags
	}
	return expandCountInstances(step, stepName, count, outputs), diags
}

func stepInstanceAddr(step runbookplanfile.Step) runbookaddrs.StepInstance {
	base := runbookaddrs.Step{Name: step.BaseName}
	if step.ForEachExpression != "" {
		return base.Instance(addrs.StringKey(step.ForEachKey))
	}
	if step.CountIndex != nil {
		return base.Instance(addrs.IntKey(*step.CountIndex))
	}
	return base.Instance(addrs.NoKey)
}

func expandForEachInstances(step *runbookconfig.Step, stepName string, val cty.Value, outputs cty.Value) []runbookplanfile.Step {
	keys := []string{}
	steps := []runbookplanfile.Step{}
	if val.Type().IsMapType() || val.Type().IsObjectType() || val.Type().IsSetType() {
		vals := val.AsValueMap()
		for key := range vals {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			addr := runbookaddrs.Step{Name: stepName}.Instance(addrs.StringKey(key))
			steps = append(steps, runbookplanfile.Step{
				Name:              addr.String(),
				BaseName:          stepName,
				ForEachExpression: string(step.ForEachSrc),
				ForEachKey:        key,
				ForEachValue:      encodeForEachValue(vals[key]),
				PlannedOutputs:    encodePlannedOutputs(outputs),
				InstanceCount:     len(keys),
			})
		}
		return steps
	}
	return nil
}

func expandCountInstances(step *runbookconfig.Step, stepName string, count int, outputs cty.Value) []runbookplanfile.Step {
	steps := make([]runbookplanfile.Step, 0, count)
	for i := 0; i < count; i++ {
		idx := i
		addr := runbookaddrs.Step{Name: stepName}.Instance(addrs.IntKey(i))
		steps = append(steps, runbookplanfile.Step{
			Name:            addr.String(),
			BaseName:        stepName,
			CountExpression: string(step.CountSrc),
			CountIndex:      &idx,
			PlannedOutputs:  encodePlannedOutputs(outputs),
			InstanceCount:   count,
		})
	}
	return steps
}

func singletonStepManifest(step *runbookconfig.Step, stepName string, after []string, result StepPlanResult) runbookplanfile.Step {
	return runbookplanfile.Step{
		Name:           stepName,
		BaseName:       stepName,
		PlannedOutputs: encodePlannedOutputs(result.Outputs),
		After:          append([]string(nil), after...),
		KnownSkipped:   result.KnownSkipped,
		SkipReason:     result.SkipReason,
		PlannedActions: append([]string(nil), result.PlannedActions...),
		PlannedQueries: append([]string(nil), result.PlannedQueries...),
		PlannedData:    append([]string(nil), result.PlannedData...),
		Outputs:        append([]string(nil), result.OutputNames...),
	}
}

func decodePlannedOutputsMap(raw map[string][]byte) cty.Value {
	ret := map[string]cty.Value{}
	for name, bytes := range raw {
		v, err := ctymsgpack.Unmarshal(bytes, cty.DynamicPseudoType)
		if err != nil {
			continue
		}
		ret[name] = v
	}
	if len(ret) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(ret)
}

func eachScopeForExpandedStep(step *runbookplanfile.Step) cty.Value {
	if step == nil || step.ForEachExpression == "" {
		return cty.NilVal
	}
	key := cty.StringVal(step.ForEachKey)
	value := key
	if len(step.ForEachValue) != 0 {
		if decoded, err := ctymsgpack.Unmarshal(step.ForEachValue, cty.DynamicPseudoType); err == nil {
			value = decoded
		}
	}
	return cty.ObjectVal(map[string]cty.Value{
		"key":   key,
		"value": value,
	})
}

func countScopeForExpandedStep(step *runbookplanfile.Step) cty.Value {
	if step == nil || step.CountIndex == nil {
		return cty.NilVal
	}
	return cty.ObjectVal(map[string]cty.Value{
		"index": cty.NumberIntVal(int64(*step.CountIndex)),
	})
}

func evaluateCountExpression(expr hcl.Expression, scope runbookconfig.EvalScope) (int, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if expr == nil {
		return 0, diags
	}
	val, evalDiags := runbookconfig.EvalExpr(expr, scope, cty.Number)
	diags = diags.Append(evalDiags)
	if evalDiags.HasErrors() {
		return 0, diags
	}
	if val == cty.NilVal || val.IsNull() {
		return 0, diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Invalid count argument", Detail: `The given "count" argument value is null. An integer is required.`, Subject: expr.Range().Ptr()})
	}
	if !val.IsKnown() {
		return 0, diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Invalid count argument", Detail: `The "count" value depends on values that cannot be determined yet.`, Subject: expr.Range().Ptr()})
	}
	if val.Type() != cty.Number {
		return 0, diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Invalid count argument", Detail: fmt.Sprintf(`The given "count" argument value is unsuitable: must be a number, got %s.`, val.Type().FriendlyName()), Subject: expr.Range().Ptr()})
	}
	bf := val.AsBigFloat()
	i, acc := bf.Int64()
	if acc != big.Exact {
		return 0, diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Invalid count argument", Detail: `The given "count" argument value is unsuitable: must be a whole number.`, Subject: expr.Range().Ptr()})
	}
	if i < 0 {
		return 0, diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Invalid count argument", Detail: `The given "count" argument value is unsuitable: must be greater than or equal to zero.`, Subject: expr.Range().Ptr()})
	}
	return int(i), diags
}

func normalizeScopeValue(v cty.Value) cty.Value {
	if v == cty.NilVal {
		return cty.EmptyObjectVal
	}
	return v
}

func encodeForEachValue(v cty.Value) []byte {
	encoded, err := ctymsgpack.Marshal(v, cty.DynamicPseudoType)
	if err != nil {
		return nil
	}
	return encoded
}

func listScopeFromQueries(step *runbookconfig.Step, queries []runbookconfig.PlannedQuery) cty.Value {
	if step == nil || len(step.Lists) == 0 || len(queries) == 0 {
		return cty.NilVal
	}
	byType := map[string]map[string]cty.Value{}
	for _, list := range step.Lists {
		if list == nil {
			continue
		}
		want := fmt.Sprintf("list.%s.%s", list.Type, list.Name)
		for _, query := range queries {
			if query.Data == cty.NilVal {
				continue
			}
			if query.Address != want && !strings.HasSuffix(query.Address, want) && !(len(step.Lists) == 1 && len(queries) == 1) {
				continue
			}
			if _, ok := byType[list.Type]; !ok {
				byType[list.Type] = map[string]cty.Value{}
			}
			byType[list.Type][list.Name] = cty.ObjectVal(map[string]cty.Value{"data": query.Data})
		}
	}
	if len(byType) == 0 {
		return cty.NilVal
	}
	ret := map[string]cty.Value{}
	for typeName, vals := range byType {
		ret[typeName] = cty.ObjectVal(vals)
	}
	return cty.ObjectVal(ret)
}

func evaluateOutputsWithListScope(step *runbookconfig.Step, varScope cty.Value, stepsScope cty.Value, workspaceScope cty.Value, listScope cty.Value) cty.Value {
	if step == nil || len(step.Outputs) == 0 {
		return cty.EmptyObjectVal
	}
	vals := map[string]cty.Value{}
	ctx := &hcl.EvalContext{Variables: map[string]cty.Value{
		"var":       normalizeScopeValue(varScope),
		"steps":     normalizeScopeValue(stepsScope),
		"workspace": normalizeScopeValue(workspaceScope),
		"list":      normalizeScopeValue(listScope),
	}, Functions: lang.TestingFunctions()}
	for name, output := range step.Outputs {
		if output == nil || output.Value == nil {
			continue
		}
		val, diags := output.Value.Value(ctx)
		if diags.HasErrors() {
			continue
		}
		vals[name] = val
	}
	if len(vals) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(vals)
}

func mergeOutputs(primary, fallback cty.Value) cty.Value {
	ret := map[string]cty.Value{}
	if fallback != cty.NilVal && fallback.Type().IsObjectType() {
		for k, v := range fallback.AsValueMap() {
			ret[k] = v
		}
	}
	if primary != cty.NilVal && primary.Type().IsObjectType() {
		for k, v := range primary.AsValueMap() {
			ret[k] = v
		}
	}
	if len(ret) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(ret)
}
