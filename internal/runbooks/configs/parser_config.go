package runbookconfigs

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/configs"
)

func (p *RunbookParser) parseRunbookConfigFile(body hcl.Body, diags hcl.Diagnostics) (*RunbookFile, hcl.Diagnostics) {
	file := &RunbookFile{}

	var reqDiags hcl.Diagnostics
	file.CoreVersionConstraints, reqDiags = sniffCoreVersionRequirements(body)
	diags = append(diags, reqDiags...)

	content, contentDiags := body.Content(runbookConfigFileSchema)
	diags = append(diags, contentDiags...)

	for _, block := range content.Blocks {
		switch block.Type {
		case "runbook":
			content, contentDiags := block.Body.Content(runbookBlockSchema)
			diags = append(diags, contentDiags...)

			for _, innerBlock := range content.Blocks {
				switch innerBlock.Type {
				case "required_providers":
					reqProvs, reqProvDiags := configs.DecodeRequiredProvidersBlock(innerBlock)
					diags = append(diags, reqProvDiags...)
					if !reqProvDiags.HasErrors() {
						file.RequiredProviders = append(file.RequiredProviders, reqProvs)
					}
				default:
					// Should never happen
					continue
				}
			}
		case "provider":
			// Hard coding test file to false
			prov, provDiags := configs.DecodeProviderBlock(block, false)
			diags = append(diags, provDiags...)
			if prov != nil {
				file.ProviderConfigs = append(file.ProviderConfigs, prov)
			}
		case "variable":
			// No overrides
			vars, varDiags := configs.DecodeVariableBlock(block, false)
			diags = append(diags, varDiags...)
			if vars != nil {
				file.Variables = append(file.Variables, vars)
			}

		case "output":
			// No overrides
			cfg, cfgDiags := configs.DecodeOutputBlock(block, false)
			diags = append(diags, cfgDiags...)
			if cfg != nil {
				file.Outputs = append(file.Outputs, cfg)
			}

		case "step":
			step, stepDiags := decodeStepBlock(block)
			diags = append(diags, stepDiags...)
			if step != nil {
				file.Steps = append(file.Steps, step)
			}
		}
	}

	return file, diags
}

func sniffCoreVersionRequirements(body hcl.Body) ([]configs.VersionConstraint, hcl.Diagnostics) {
	rootContent, _, diags := body.PartialContent(runbookConfigFileTerraformBlockSniffRootSchema)

	var constraints []configs.VersionConstraint

	for _, block := range rootContent.Blocks {
		content, _, blockDiags := block.Body.PartialContent(runbookConfigFileVersionSniffBlockSchema)
		diags = append(diags, blockDiags...)

		attr, exists := content.Attributes["required_version"]
		if !exists {
			continue
		}

		constraint, constraintDiags := configs.DecodeVersionConstraint(attr)
		diags = append(diags, constraintDiags...)
		if !constraintDiags.HasErrors() {
			constraints = append(constraints, constraint)
		}
	}

	return constraints, diags
}

var runbookConfigFileSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "runbook"},
		{
			Type:       "provider",
			LabelNames: []string{"name"},
		},
		{
			Type:       "variable",
			LabelNames: []string{"name"},
		},
		{
			Type:       "output",
			LabelNames: []string{"name"},
		},
		{
			Type:       "step",
			LabelNames: []string{"name"},
		},
	},
}

var runbookBlockSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "required_version"},
	},
	Blocks: []hcl.BlockHeaderSchema{
		{
			Type: "required_providers",
		},
	},
}

var runbookConfigFileTerraformBlockSniffRootSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{
			Type: "runbook",
		},
	},
}

// configFileVersionSniffBlockSchema is a schema for sniffCoreVersionRequirements
var runbookConfigFileVersionSniffBlockSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{
			Name: "required_version",
		},
	},
}

var invokeActionSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{
			Name:     "action",
			Required: true,
		},
	},
}
