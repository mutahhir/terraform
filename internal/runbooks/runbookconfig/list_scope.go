// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import "github.com/zclconf/go-cty/cty"

func ScopeWithStepLists(step *Step, scope EvalScope) EvalScope {
	if step == nil || len(step.Lists) == 0 {
		return scope
	}

	current := normalizeScopeValue(scope.List)
	merged := map[string]cty.Value{}
	if current.Type().IsObjectType() {
		for k, v := range current.AsValueMap() {
			merged[k] = v
		}
	}

	for _, list := range step.Lists {
		if list == nil {
			continue
		}
		typeMap, ok := merged[list.Type]
		var typeVals map[string]cty.Value
		if ok && typeMap.Type().IsObjectType() {
			typeVals = make(map[string]cty.Value)
			for k, v := range typeMap.AsValueMap() {
				typeVals[k] = v
			}
		} else {
			typeVals = make(map[string]cty.Value)
		}
		if _, exists := typeVals[list.Name]; !exists {
			typeVals[list.Name] = cty.ObjectVal(map[string]cty.Value{
				"data": cty.ListValEmpty(cty.DynamicPseudoType),
			})
		}
		merged[list.Type] = cty.ObjectVal(typeVals)
	}

	scope.List = cty.ObjectVal(merged)
	return scope
}
