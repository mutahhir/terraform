// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookplan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/lang"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/runbooks/runbookplanfile"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
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
	StepResults map[string]cty.Value
}

func Build(cfg *runbookconfig.Config, configPath, workspace string, stepOrder []string, deps map[string][]string, varScope cty.Value, workspaceScope cty.Value, planner StepPlanner) (*BuildResult, tfdiags.Diagnostics, error) {
	var diags tfdiags.Diagnostics
	manifest := &runbookplanfile.Plan{ConfigPath: configPath, Workspace: workspace}
	lowered := make(map[string]map[string][]byte)
	stepResults := make(map[string]cty.Value)
	rawSteps := stepsByName(cfg)

	for _, stepName := range stepOrder {
		step := rawSteps[stepName]
		if step == nil {
			continue
		}
		result, err := planner(stepName, runbookconfig.EvalScope{
			Variables: varScope,
			Steps:     cty.ObjectVal(stepResults),
			Workspace: workspaceScope,
		})
		if err != nil {
			return nil, diags, err
		}
		if len(result.LoweredFiles) > 0 {
			lowered[stepName] = result.LoweredFiles
		}
		result.Outputs = mergeOutputs(result.Outputs, StepOutputsFromQueries(step, result.Queries, varScope, cty.ObjectVal(stepResults), workspaceScope))
		stepResults[stepName] = result.Outputs

		if step.ForEach != nil {
			expanded, keys, moreDiags := expandStepInstances(step, stepResults, workspaceScope, varScope)
			diags = diags.Append(moreDiags)
			if moreDiags.HasErrors() {
				return nil, diags, nil
			}
			if len(result.LoweredFiles) > 0 {
				for _, instanceName := range expanded {
					lowered[instanceName] = result.LoweredFiles
				}
			}
			for i, instanceName := range expanded {
				manifest.StepOrder = append(manifest.StepOrder, instanceName)
				manifest.Steps = append(manifest.Steps, runbookplanfile.Step{
					Name:           instanceName,
					BaseName:       stepName,
					ForEachKey:     keys[i],
					InstanceCount:  len(expanded),
					After:          append([]string(nil), deps[stepName]...),
					KnownSkipped:   result.KnownSkipped,
					SkipReason:     result.SkipReason,
					PlannedActions: append([]string(nil), result.PlannedActions...),
					PlannedQueries: append([]string(nil), result.PlannedQueries...),
					PlannedData:    append([]string(nil), result.PlannedData...),
					Outputs:        append([]string(nil), result.OutputNames...),
				})
			}
			continue
		}

		manifest.StepOrder = append(manifest.StepOrder, stepName)
		manifest.Steps = append(manifest.Steps, runbookplanfile.Step{
			Name:           stepName,
			After:          append([]string(nil), deps[stepName]...),
			KnownSkipped:   result.KnownSkipped,
			SkipReason:     result.SkipReason,
			PlannedActions: append([]string(nil), result.PlannedActions...),
			PlannedQueries: append([]string(nil), result.PlannedQueries...),
			PlannedData:    append([]string(nil), result.PlannedData...),
			Outputs:        append([]string(nil), result.OutputNames...),
		})
	}

	return &BuildResult{Manifest: manifest, Lowered: lowered, StepResults: stepResults}, diags, nil
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

func expandStepInstances(step *runbookconfig.Step, plannedSteps map[string]cty.Value, workspaceScope cty.Value, varScope cty.Value) ([]string, []string, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if step == nil || step.ForEach == nil {
		return nil, nil, diags
	}
	ctx := &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"steps":     normalizeScopeValue(cty.ObjectVal(plannedSteps)),
			"workspace": normalizeScopeValue(workspaceScope),
			"var":       normalizeScopeValue(varScope),
		},
		Functions: lang.TestingFunctions(),
	}
	val, hclDiags := step.ForEach.Value(ctx)
	diags = diags.Append(hclDiags)
	if hclDiags.HasErrors() || val == cty.NilVal || !val.IsKnown() || val.IsNull() {
		return nil, nil, diags
	}

	keys := []string{}
	instances := []string{}
	if val.Type().IsMapType() || val.Type().IsObjectType() {
		for key := range val.AsValueMap() {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			instances = append(instances, fmt.Sprintf("%s[%s]", step.Name, sanitizeKey(key)))
		}
		return instances, keys, diags
	}
	if val.Type().IsTupleType() || val.Type().IsListType() || val.Type().IsSetType() {
		it := val.ElementIterator()
		idx := 0
		for it.Next() {
			_, elem := it.Element()
			key := sanitizeKey(strings.Trim(tfdiags.CompactValueStr(elem), "\""))
			if key == "" {
				key = fmt.Sprintf("item-%d", idx)
			}
			keys = append(keys, key)
			instances = append(instances, fmt.Sprintf("%s[%s]", step.Name, key))
			idx++
		}
	}
	return instances, keys, diags
}

func normalizeScopeValue(v cty.Value) cty.Value {
	if v == cty.NilVal {
		return cty.EmptyObjectVal
	}
	return v
}

func sanitizeKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "item"
	}
	replacer := strings.NewReplacer(" ", "-", "/", "-", ":", "-", "[", "-", "]", "-")
	return replacer.Replace(key)
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
