// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/spf13/afero"
	"github.com/zclconf/go-cty/cty"

	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type LoweredStepBundle struct {
	StepName string
	Dir      string
	Files    map[string][]byte
}

func LowerStep(cfg *Config, step *Step) (*LoweredStepBundle, tfdiags.Diagnostics) {
	return LowerStepWithScope(cfg, step, EvalScope{})
}

func LowerStepWithScope(cfg *Config, step *Step, scope EvalScope) (*LoweredStepBundle, tfdiags.Diagnostics) {
	return LowerStepInstanceWithScope(cfg, step, scope)
}

func LowerStepInstance(cfg *Config, step *Step, each cty.Value, count cty.Value) (*LoweredStepBundle, tfdiags.Diagnostics) {
	return LowerStepInstanceWithScope(cfg, step, EvalScope{Each: each, Count: count})
}

func LowerStepInstanceWithScope(cfg *Config, step *Step, scope EvalScope) (*LoweredStepBundle, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if cfg == nil || step == nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Cannot lower step", "Runbook configuration or step is missing."))
	}

	dir, err := os.MkdirTemp("", "terraform-runbook-step-")
	if err != nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Cannot lower step", err.Error()))
	}

	files := make(map[string][]byte)
	mainSrc, mainDiags := buildMainTF(cfg, step, scope)
	diags = diags.Append(mainDiags)
	if len(bytes.TrimSpace(mainSrc)) > 0 {
		files["main.tf"] = mainSrc
	}
	querySrc, queryDiags := buildQueryTF(step)
	diags = diags.Append(queryDiags)
	if len(bytes.TrimSpace(querySrc)) > 0 {
		files["main.tfquery.hcl"] = querySrc
	}

	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), src, 0644); err != nil {
			return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Cannot write lowered step file", err.Error()))
		}
	}

	parser := configs.NewParser(afero.NewOsFs())
	for name, src := range files {
		parser.ForceFileSource(filepath.Join(dir, name), src)
	}
	_, hclDiags := parser.LoadConfigDir(dir)
	diags = diags.Append(hclDiags)

	return &LoweredStepBundle{StepName: step.Name, Dir: dir, Files: files}, diags
}

func buildMainTF(cfg *Config, step *Step, scope EvalScope) ([]byte, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	mainFile := hclwrite.NewEmptyFile()
	rootBody := mainFile.Body()
	lowerScope := NewLowerScope(scope)
	if cfg.Runbook != nil {
		settingsDiags := appendTerraformSettings(rootBody, cfg)
		diags = diags.Append(settingsDiags)
		variableDiags := appendVariableBlocks(rootBody, cfg)
		diags = diags.Append(variableDiags)
		providerDiags := appendProviderBlocks(rootBody, cfg)
		diags = diags.Append(providerDiags)
		actionDiags := appendRootActionBlocks(rootBody, cfg, lowerScope)
		diags = diags.Append(actionDiags)
	}
	for _, action := range step.Actions {
		if action == nil || len(bytes.TrimSpace(action.Src)) == 0 {
			continue
		}
		src := action.Src
		src = rewriteRepetitionReferences(src, scope.Each, scope.Count)
		parsed, parseDiags := hclwrite.ParseConfig(src, action.DeclRange.Filename, hcl.InitialPos)
		if parseDiags.HasErrors() || parsed == nil {
			diags = diags.Append(parseDiags)
			continue
		}
		diags = diags.Append(lowerScope.RewriteBodyExpressions(parsed.Body()))
		rootBody.AppendUnstructuredTokens(parsed.Body().BuildTokens(nil))
		rootBody.AppendNewline()
	}
	for _, dataSource := range step.DataSources {
		if dataSource == nil || len(bytes.TrimSpace(dataSource.Src)) == 0 {
			continue
		}
		src := dataSource.Src
		src = rewriteRepetitionReferences(src, scope.Each, scope.Count)
		parsed, parseDiags := hclwrite.ParseConfig(src, dataSource.DeclRange.Filename, hcl.InitialPos)
		if parseDiags.HasErrors() || parsed == nil {
			diags = diags.Append(parseDiags)
			continue
		}
		diags = diags.Append(lowerScope.RewriteBodyExpressions(parsed.Body()))
		rootBody.AppendUnstructuredTokens(parsed.Body().BuildTokens(nil))
		rootBody.AppendNewline()
	}
	outputDiags := appendStepOutputBlocks(rootBody, step, lowerScope)
	diags = diags.Append(outputDiags)
	conditionDiags := appendConditionOutputBlocks(rootBody, step, lowerScope)
	diags = diags.Append(conditionDiags)
	syntheticVarDiags := lowerScope.AppendVariableBlocks(rootBody)
	diags = diags.Append(syntheticVarDiags)
	return hclwrite.Format(mainFile.Bytes()), diags
}

func buildQueryTF(step *Step) ([]byte, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if step == nil || len(step.Lists) == 0 {
		return nil, diags
	}
	var buf bytes.Buffer
	for _, list := range step.Lists {
		if list == nil || len(bytes.TrimSpace(list.Src)) == 0 {
			continue
		}
		buf.Write(bytes.TrimSpace(list.Src))
		buf.WriteString("\n\n")
	}
	return buf.Bytes(), diags
}

func appendTerraformSettings(body *hclwrite.Body, cfg *Config) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	runbook := cfg.Runbook
	terraformBlock := body.AppendNewBlock("terraform", nil)
	terraformBody := terraformBlock.Body()
	if runbook.TerraformVersion != "" {
		terraformBody.SetAttributeValue("required_version", cty.StringVal(runbook.TerraformVersion))
	}
	if len(runbook.RequiredProviders) == 0 {
		return diags
	}
	var rpSrc strings.Builder
	rpSrc.WriteString("required_providers {\n")
	providerNames := make([]string, 0, len(runbook.RequiredProviders))
	requiredProviders := make(map[string]*RequiredProvider, len(runbook.RequiredProviders))
	for _, provider := range runbook.RequiredProviders {
		if provider == nil {
			continue
		}
		providerNames = append(providerNames, provider.Name)
		requiredProviders[provider.Name] = provider
	}
	sort.Strings(providerNames)
	for _, name := range providerNames {
		req := requiredProviders[name]
		if req == nil {
			continue
		}
		rpSrc.WriteString(fmt.Sprintf("  %s = {\n", name))
		rpSrc.WriteString(fmt.Sprintf("    source = %q\n", req.Source))
		rpSrc.WriteString("  }\n")
	}
	rpSrc.WriteString("}\n")
	parsed, _ := hclwrite.ParseConfig([]byte(rpSrc.String()), "required_providers.hcl", hcl.InitialPos)
	if parsed != nil {
		terraformBody.AppendUnstructuredTokens(parsed.Body().BuildTokens(nil))
	}
	return diags
}

func appendRootActionBlocks(body *hclwrite.Body, cfg *Config, lowerScope *LowerScope) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if cfg == nil {
		return diags
	}
	actionsByRef, workspaceDiags := WorkspaceActions(cfg)
	diags = diags.Append(workspaceDiags)
	for ref, action := range cfg.Actions {
		if action != nil {
			actionsByRef[ref] = action
		}
	}
	actions := make([]*Action, 0, len(actionsByRef))
	for _, action := range actionsByRef {
		if action != nil {
			actions = append(actions, action)
		}
	}
	sort.Slice(actions, func(i, j int) bool {
		if actions[i].Type != actions[j].Type {
			return actions[i].Type < actions[j].Type
		}
		return actions[i].Name < actions[j].Name
	})
	for _, action := range actions {
		if len(bytes.TrimSpace(action.Src)) == 0 {
			continue
		}
		parsed, parseDiags := hclwrite.ParseConfig(action.Src, action.DeclRange.Filename, hcl.InitialPos)
		if parseDiags.HasErrors() || parsed == nil {
			diags = diags.Append(parseDiags)
			continue
		}
		if lowerScope != nil {
			diags = diags.Append(lowerScope.RewriteBodyExpressions(parsed.Body()))
		}
		body.AppendUnstructuredTokens(parsed.Body().BuildTokens(nil))
		body.AppendNewline()
	}
	return diags
}

func appendStepOutputBlocks(body *hclwrite.Body, step *Step, lowerScope *LowerScope) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if step == nil || len(step.Outputs) == 0 {
		return diags
	}
	outputNames := make([]string, 0, len(step.Outputs))
	for name := range step.Outputs {
		outputNames = append(outputNames, name)
	}
	sort.Strings(outputNames)
	for _, name := range outputNames {
		output := step.Outputs[name]
		if output == nil || len(bytes.TrimSpace(output.ValueSrc)) == 0 {
			continue
		}
		valueSrc := bytes.TrimSpace(output.ValueSrc)
		rewrittenSrc, rewriteDiags := lowerScope.RewriteExpr(output.Value, valueSrc)
		diags = diags.Append(rewriteDiags)
		valueSrc = bytes.TrimSpace(rewriteRepetitionReferences(rewrittenSrc, lowerScope.scope.Each, lowerScope.scope.Count))
		blockSrc := fmt.Sprintf("output %q {\n  value = %s\n}\n", name, string(valueSrc))
		parsed, parseDiags := hclwrite.ParseConfig([]byte(blockSrc), output.DeclRange.Filename, hcl.InitialPos)
		if parseDiags.HasErrors() || parsed == nil {
			diags = diags.Append(parseDiags)
			continue
		}
		body.AppendUnstructuredTokens(parsed.Body().BuildTokens(nil))
		body.AppendNewline()
	}
	return diags
}

func appendConditionOutputBlocks(body *hclwrite.Body, step *Step, lowerScope *LowerScope) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if step == nil {
		return diags
	}
	append := func(kind string, conds []*Condition) {
		for i, cond := range conds {
			if cond == nil {
				continue
			}
			if len(bytes.TrimSpace(cond.ConditionSrc)) > 0 {
				conditionSrc := bytes.TrimSpace(cond.ConditionSrc)
				rewrittenSrc, rewriteDiags := lowerScope.RewriteExpr(cond.Condition, conditionSrc)
				diags = diags.Append(rewriteDiags)
				conditionSrc = bytes.TrimSpace(rewriteRepetitionReferences(rewrittenSrc, lowerScope.scope.Each, lowerScope.scope.Count))
				blockSrc := fmt.Sprintf("output %q {\n  value = %s\n}\n", fmt.Sprintf("__runbook_%s_%d_condition", kind, i), string(conditionSrc))
				parsed, parseDiags := hclwrite.ParseConfig([]byte(blockSrc), cond.DeclRange.Filename, hcl.InitialPos)
				if parseDiags.HasErrors() || parsed == nil {
					diags = diags.Append(parseDiags)
				} else {
					body.AppendUnstructuredTokens(parsed.Body().BuildTokens(nil))
					body.AppendNewline()
				}
			}
			if len(bytes.TrimSpace(cond.ErrorMessageSrc)) > 0 {
				errorSrc := bytes.TrimSpace(cond.ErrorMessageSrc)
				rewrittenSrc, rewriteDiags := lowerScope.RewriteExpr(cond.ErrorMessage, errorSrc)
				diags = diags.Append(rewriteDiags)
				errorSrc = bytes.TrimSpace(rewriteRepetitionReferences(rewrittenSrc, lowerScope.scope.Each, lowerScope.scope.Count))
				blockSrc := fmt.Sprintf("output %q {\n  value = %s\n}\n", fmt.Sprintf("__runbook_%s_%d_error_message", kind, i), string(errorSrc))
				parsed, parseDiags := hclwrite.ParseConfig([]byte(blockSrc), cond.DeclRange.Filename, hcl.InitialPos)
				if parseDiags.HasErrors() || parsed == nil {
					diags = diags.Append(parseDiags)
				} else {
					body.AppendUnstructuredTokens(parsed.Body().BuildTokens(nil))
					body.AppendNewline()
				}
				body.AppendNewline()
			}
		}
	}
	append("precondition", step.Preconditions)
	append("postcondition", step.Postconditions)
	return diags
}

func rewriteRepetitionReferences(src []byte, each cty.Value, count cty.Value) []byte {
	ret := string(src)
	if each != cty.NilVal && each.Type().IsObjectType() && each.Type().HasAttribute("key") {
		ret = strings.ReplaceAll(ret, "each.key", hclQuotedLiteral(each.GetAttr("key")))
	}
	if each != cty.NilVal && each.Type().IsObjectType() && each.Type().HasAttribute("value") {
		ret = strings.ReplaceAll(ret, "each.value", hclQuotedLiteral(each.GetAttr("value")))
	}
	if count != cty.NilVal && count.Type().IsObjectType() && count.Type().HasAttribute("index") {
		ret = strings.ReplaceAll(ret, "count.index", hclLiteral(count.GetAttr("index")))
	}
	return []byte(ret)
}

func hclLiteral(v cty.Value) string {
	if !v.IsKnown() || v.IsNull() {
		return "null"
	}
	return string(hclwrite.TokensForValue(v).Bytes())
}

func hclQuotedLiteral(v cty.Value) string {
	if !v.IsKnown() || v.IsNull() || v.Type() != cty.String {
		return "null"
	}
	return fmt.Sprintf("%q", v.AsString())
}

func appendProviderBlocks(body *hclwrite.Body, cfg *Config) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if cfg == nil {
		return diags
	}
	providers := make([]*ProviderConfig, 0)
	for _, file := range cfg.Files {
		for _, provider := range file.Providers {
			if provider != nil {
				providers = append(providers, provider)
			}
		}
	}
	sort.Slice(providers, func(i, j int) bool {
		if providers[i].Type != providers[j].Type {
			return providers[i].Type < providers[j].Type
		}
		return providers[i].Alias < providers[j].Alias
	})
	for _, provider := range providers {
		if len(bytes.TrimSpace(provider.Src)) == 0 {
			labels := []string{provider.Type}
			if provider.Alias != "" {
				labels = append(labels, provider.Alias)
			}
			body.AppendNewBlock("provider", labels)
			continue
		}
		parsed, parseDiags := hclwrite.ParseConfig(provider.Src, provider.DeclRange.Filename, hcl.InitialPos)
		if parseDiags.HasErrors() || parsed == nil {
			diags = diags.Append(parseDiags)
			continue
		}
		body.AppendUnstructuredTokens(parsed.Body().BuildTokens(nil))
		body.AppendNewline()
	}
	return diags
}

func appendVariableBlocks(body *hclwrite.Body, cfg *Config) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if cfg == nil {
		return diags
	}
	variables := make([]*Variable, 0, len(cfg.Variables))
	for _, variable := range cfg.Variables {
		if variable != nil {
			variables = append(variables, variable)
		}
	}
	sort.Slice(variables, func(i, j int) bool {
		return variables[i].Name < variables[j].Name
	})
	for _, variable := range variables {
		if len(bytes.TrimSpace(variable.Src)) == 0 {
			body.AppendNewBlock("variable", []string{variable.Name})
			continue
		}
		parsed, parseDiags := hclwrite.ParseConfig(variable.Src, variable.DeclRange.Filename, hcl.InitialPos)
		if parseDiags.HasErrors() || parsed == nil {
			diags = diags.Append(parseDiags)
			continue
		}
		body.AppendUnstructuredTokens(parsed.Body().BuildTokens(nil))
		body.AppendNewline()
	}
	return diags
}
