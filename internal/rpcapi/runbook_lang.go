// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package rpcapi

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/lang"
	"github.com/hashicorp/terraform/internal/providers"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

type runbookLangData struct {
	scope runbookconfig.EvalScope
}

func (d *runbookLangData) StaticValidateReferences(refs []*addrs.Reference, self addrs.Referenceable, source addrs.Referenceable) tfdiags.Diagnostics {
	return nil
}

func (d *runbookLangData) GetCountAttr(addr addrs.CountAttr, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	if d == nil || d.scope.Count == cty.NilVal || !d.scope.Count.Type().IsObjectType() || !d.scope.Count.Type().HasAttribute(addr.Name) {
		return cty.DynamicVal, nil
	}
	return d.scope.Count.GetAttr(addr.Name), nil
}

func (d *runbookLangData) GetForEachAttr(addr addrs.ForEachAttr, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	if d == nil || d.scope.Each == cty.NilVal || !d.scope.Each.Type().IsObjectType() || !d.scope.Each.Type().HasAttribute(addr.Name) {
		return cty.DynamicVal, nil
	}
	return d.scope.Each.GetAttr(addr.Name), nil
}

func (d *runbookLangData) GetResource(addr addrs.Resource, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	var scopeVal cty.Value
	switch addr.Mode {
	case addrs.ListResourceMode:
		scopeVal = d.scope.List
	default:
		return cty.DynamicVal, nil
	}
	if scopeVal == cty.NilVal || !scopeVal.Type().IsObjectType() || !scopeVal.Type().HasAttribute(addr.Type) {
		return cty.DynamicVal, nil
	}
	obj := scopeVal.GetAttr(addr.Type)
	if !obj.Type().IsObjectType() || !obj.Type().HasAttribute(addr.Name) {
		return cty.DynamicVal, nil
	}
	return obj.GetAttr(addr.Name), nil
}

func (d *runbookLangData) GetLocalValue(addr addrs.LocalValue, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	if d == nil || d.scope.Locals == cty.NilVal || !d.scope.Locals.Type().IsObjectType() || !d.scope.Locals.Type().HasAttribute(addr.Name) {
		return cty.DynamicVal, nil
	}
	return d.scope.Locals.GetAttr(addr.Name), nil
}

func (d *runbookLangData) GetModule(addr addrs.ModuleCall, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *runbookLangData) GetPathAttr(addr addrs.PathAttr, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *runbookLangData) GetTerraformAttr(addr addrs.TerraformAttr, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *runbookLangData) GetInputVariable(addr addrs.InputVariable, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	if d == nil || d.scope.Variables == cty.NilVal || !d.scope.Variables.Type().IsObjectType() || !d.scope.Variables.Type().HasAttribute(addr.Name) {
		return cty.DynamicVal, nil
	}
	return d.scope.Variables.GetAttr(addr.Name), nil
}

func (d *runbookLangData) GetOutput(addr addrs.OutputValue, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *runbookLangData) GetCheckBlock(addr addrs.Check, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *runbookLangData) GetRunBlock(addr addrs.Run, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}

func (d *runbookLangData) GetStep(addr addrs.Step, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	if d == nil || d.scope.Steps == cty.NilVal || !d.scope.Steps.Type().IsObjectType() || !d.scope.Steps.Type().HasAttribute(addr.Name) {
		return cty.DynamicVal, nil
	}
	return d.scope.Steps.GetAttr(addr.Name), nil
}

func (d *runbookLangData) GetWorkspaceOutput(addr addrs.WorkspaceOutput, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	if d == nil || d.scope.Workspace == cty.NilVal || !d.scope.Workspace.Type().IsObjectType() || !d.scope.Workspace.Type().HasAttribute("output") {
		return cty.DynamicVal, nil
	}
	outputs := d.scope.Workspace.GetAttr("output")
	if !outputs.Type().IsObjectType() || !outputs.Type().HasAttribute(addr.Name) {
		return cty.DynamicVal, nil
	}
	return outputs.GetAttr(addr.Name), nil
}

func (s *runbooksServer) runbookEvalScope(cfg *runbookconfig.Config, scope runbookconfig.EvalScope, providerFactories map[addrs.Provider]providers.Factory) (*lang.Scope, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	scope.Variables = mergeRunbookVariableDefaults(cfg, scope.Variables)
	ret := &lang.Scope{
		Data:     &runbookLangData{scope: scope},
		ParseRef: addrs.ParseRefFromRunbookScope,
	}
	if cfg == nil || cfg.Runbook == nil {
		return ret, diags
	}
	extFuncs := lang.ExternalFuncs{Provider: map[string]map[string]function.Function{}}
	for _, req := range cfg.Runbook.RequiredProviders {
		if req == nil {
			continue
		}
		providerAddr, moreDiags := addrs.ParseProviderSourceString(req.Source)
		diags = diags.Append(moreDiags)
		if moreDiags.HasErrors() {
			continue
		}
		factory := providerFactories[providerAddr]
		if factory == nil {
			continue
		}
		provider, err := factory()
		if err != nil {
			diags = diags.Append(tfdiags.Sourceless(tfdiags.Error, "Failed to initialize provider functions", err.Error()))
			continue
		}
		decls := provider.GetProviderSchema().Functions
		if len(decls) == 0 {
			continue
		}
		funcs := map[string]function.Function{}
		for name, decl := range decls {
			decl := decl
			funcs[name] = decl.BuildFunction(providerAddr, name, nil, func() (providers.Interface, error) {
				return provider, nil
			})
		}
		extFuncs.Provider[req.Name] = funcs
	}
	ret.ExternalFuncs = extFuncs
	return ret, diags
}

func mergeRunbookVariableDefaults(cfg *runbookconfig.Config, existing cty.Value) cty.Value {
	vals := map[string]cty.Value{}
	if existing != cty.NilVal && existing.Type().IsObjectType() {
		for name, val := range existing.AsValueMap() {
			vals[name] = val
		}
	}
	if cfg == nil {
		if len(vals) == 0 {
			return cty.EmptyObjectVal
		}
		return cty.ObjectVal(vals)
	}
	for name, variable := range cfg.Variables {
		if _, exists := vals[name]; exists {
			continue
		}
		if variable != nil && variable.Default != nil {
			if val, diags := variable.Default.Value(&hcl.EvalContext{}); !diags.HasErrors() {
				vals[name] = val
				continue
			}
		}
		vals[name] = cty.NullVal(cty.DynamicPseudoType)
	}
	if len(vals) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(vals)
}

func evaluateRunbookOutputsWithTerraformScope(scope *lang.Scope, step *runbookconfig.Step) (map[string]cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := map[string]cty.Value{}
	if scope == nil || step == nil {
		return ret, diags
	}
	for name, output := range step.Outputs {
		if output == nil || output.Value == nil {
			continue
		}
		if runbookconfig.ExprReferencesRunbookActionOutput(output.Value) {
			continue
		}
		val, moreDiags := scope.EvalExpr(output.Value, cty.DynamicPseudoType)
		if moreDiags.HasErrors() {
			continue
		}
		diags = diags.Append(moreDiags)
		ret[name] = val
	}
	return ret, diags
}

func runbookProviderFactories(s *runbooksServer, runtime *runbookRuntime) (map[addrs.Provider]providers.Factory, error) {
	if s != nil && s.providerCacheOverride != nil {
		return s.providerCacheOverride, nil
	}
	if runtime == nil {
		return map[addrs.Provider]providers.Factory{}, nil
	}
	return providerFactoriesForLocks(runtime.Locks, runtime.ProviderCache)
}

func missingRunbookValue(kind, name string) tfdiags.Diagnostics {
	return tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(tfdiags.Error, fmt.Sprintf("Unknown runbook %s reference", kind), fmt.Sprintf("No value is available for %s %q in this runbook evaluation scope.", kind, name)))
}
