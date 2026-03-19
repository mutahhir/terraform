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
		settingsDiags := appendTerraformSettings(rootBody, cfg)
		diags = diags.Append(settingsDiags)
		variableDiags := appendVariableBlocks(rootBody, cfg)
		diags = diags.Append(variableDiags)
		providerDiags := appendProviderBlocks(rootBody, cfg)
		diags = diags.Append(providerDiags)
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
