// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"bytes"
	"fmt"
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

	files := make(map[string][]byte)
	mainSrc, mainDiags := buildMainTF(cfg, step)
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

func buildMainTF(cfg *Config, step *Step) ([]byte, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	mainFile := hclwrite.NewEmptyFile()
	rootBody := mainFile.Body()
	if cfg.Runbook != nil {
		appendTerraformSettings(rootBody, cfg.Runbook)
		appendProviderBlocks(rootBody, cfg.Runbook)
	}
	for _, action := range step.Actions {
		if action == nil || len(bytes.TrimSpace(action.Src)) == 0 {
			continue
		}
		parsed, parseDiags := hclwrite.ParseConfig(action.Src, action.DeclRange.Filename, hcl.InitialPos)
		if parseDiags.HasErrors() || parsed == nil {
			diags = diags.Append(parseDiags)
			continue
		}
		rootBody.AppendUnstructuredTokens(parsed.Body().BuildTokens(nil))
		rootBody.AppendNewline()
	}
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
		valueBody.SetAttributeValue("source", cty.StringVal(providerSourceForName(name)))
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

func providerSourceForName(name string) string {
	if name == "bufo" {
		return "austinvalle/bufo"
	}
	return fmt.Sprintf("hashicorp/%s", name)
}
