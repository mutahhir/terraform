// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"os"
	"path/filepath"
	"sort"

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
	var diags tfdiags.Diagnostics
	if cfg == nil || step == nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Cannot lower step", "Runbook configuration or step is missing."))
	}

	dir, err := os.MkdirTemp("", "terraform-runbook-step-")
	if err != nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Cannot lower step", err.Error()))
	}

	mainFile := hclwrite.NewEmptyFile()
	rootBody := mainFile.Body()

	if cfg.Runbook != nil {
		appendTerraformSettings(rootBody, cfg.Runbook)
		appendProviderBlocks(rootBody, cfg.Runbook)
	}
	for _, list := range step.Lists {
		appendListBlock(rootBody, list)
	}
	for _, action := range step.Actions {
		appendActionBlock(rootBody, action)
	}

	mainSrc := hclwrite.Format(mainFile.Bytes())
	mainPath := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(mainPath, mainSrc, 0644); err != nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Cannot write lowered step file", err.Error()))
	}

	bundle := &LoweredStepBundle{
		StepName: step.Name,
		Dir:      dir,
		Files: map[string][]byte{
			"main.tf": mainSrc,
		},
	}

	parser := configs.NewParser(afero.NewOsFs())
	parser.ForceFileSource(mainPath, mainSrc)
	_, hclDiags := parser.LoadConfigDir(dir)
	diags = diags.Append(hclDiags)

	return bundle, diags
}

func appendTerraformSettings(body *hclwrite.Body, runbook *Runbook) {
	terraformBlock := body.AppendNewBlock("terraform", nil)
	terraformBody := terraformBlock.Body()
	if runbook.TerraformVersion != "" {
		terraformBody.SetAttributeValue("required_version", cty.StringVal(runbook.TerraformVersion))
	}
	if len(runbook.Providers) == 0 {
		return
	}
	requiredProviders := terraformBody.AppendNewBlock("required_providers", nil)
	rpBody := requiredProviders.Body()
	providerNames := make([]string, 0, len(runbook.Providers))
	for _, provider := range runbook.Providers {
		providerNames = append(providerNames, provider.Type)
	}
	sort.Strings(providerNames)
	for _, name := range providerNames {
		valueFile := hclwrite.NewEmptyFile()
		valueBody := valueFile.Body()
		valueBody.SetAttributeValue("source", cty.StringVal("hashicorp/"+name))
		rpBody.SetAttributeRaw(name, valueBody.BuildTokens(nil))
	}
}

func appendProviderBlocks(body *hclwrite.Body, runbook *Runbook) {
	for _, provider := range runbook.Providers {
		if provider == nil {
			continue
		}
		body.AppendNewBlock("provider", []string{provider.Type})
	}
}

func appendActionBlock(body *hclwrite.Body, action *Action) {
	if action == nil {
		return
	}
	block := body.AppendNewBlock("action", []string{action.Type, action.Name})
	if action.Config == nil {
		return
	}
	content, _, _ := action.Config.PartialContent(&hcl.BodySchema{})
	if content == nil {
		return
	}
	for name, attr := range content.Attributes {
		block.Body().SetAttributeRaw(name, expressionTokens(attr.Expr))
	}
	for _, nested := range content.Blocks {
		appendNestedBlock(block.Body(), nested)
	}
}

func appendListBlock(body *hclwrite.Body, list *List) {
	if list == nil {
		return
	}
	block := body.AppendNewBlock("list", []string{list.Type, list.Name})
	if list.Config == nil {
		return
	}
	content, _, _ := list.Config.PartialContent(&hcl.BodySchema{})
	if content == nil {
		return
	}
	for name, attr := range content.Attributes {
		block.Body().SetAttributeRaw(name, expressionTokens(attr.Expr))
	}
	for _, nested := range content.Blocks {
		appendNestedBlock(block.Body(), nested)
	}
}

func appendNestedBlock(body *hclwrite.Body, block *hcl.Block) {
	if block == nil {
		return
	}
	newBlock := body.AppendNewBlock(block.Type, block.Labels)
	content, _, _ := block.Body.PartialContent(&hcl.BodySchema{})
	if content == nil {
		return
	}
	for name, attr := range content.Attributes {
		newBlock.Body().SetAttributeRaw(name, expressionTokens(attr.Expr))
	}
	for _, nested := range content.Blocks {
		appendNestedBlock(newBlock.Body(), nested)
	}
}

func expressionTokens(expr hcl.Expression) hclwrite.Tokens {
	if expr == nil {
		return nil
	}
	return hclwrite.TokensForValue(cty.StringVal(expr.Range().String()))
}
