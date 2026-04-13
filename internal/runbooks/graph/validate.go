package runbookgraph

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/lang"
	"github.com/hashicorp/terraform/internal/providers"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type ValidateOpts struct {
	Providers map[addrs.Provider]providers.Factory
}

type ProviderValidationContext interface {
	ProviderInput(addrs.AbsProviderConfig) map[string]cty.Value
	EvaluateBlock(hcl.Body, *configschema.Block) (cty.Value, hcl.Body, tfdiags.Diagnostics)
}

func Validate(config *runbookconfigs.RunbookConfig, opts *ValidateOpts) tfdiags.Diagnostics {
	ctx := NewEvalContext(EvalContextOpts{Config: config})
	return ValidateWithContext(config, ctx, opts)
}

func ValidateWithContext(config *runbookconfigs.RunbookConfig, ctx ProviderValidationContext, opts *ValidateOpts) tfdiags.Diagnostics {
	if config == nil {
		return nil
	}
	if ctx == nil {
		ctx = NewEvalContext(EvalContextOpts{Config: config})
	}

	var diags tfdiags.Diagnostics
	for _, providerConfig := range config.ProviderConfigs {
		diags = diags.Append(validateProviderConfig(config, ctx, providerConfig, opts))
	}
	return diags
}

func validateProviderConfig(config *runbookconfigs.RunbookConfig, ctx ProviderValidationContext, providerConfig *configs.Provider, opts *ValidateOpts) tfdiags.Diagnostics {
	if providerConfig == nil {
		return nil
	}

	providerType := providerTypeForConfig(config, providerConfig)
	factory := providerFactory(providerType, opts)
	if factory == nil {
		return tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Missing provider implementation",
			fmt.Sprintf("No provider factory is configured for %s.", providerType),
		))
	}

	provider, err := factory()
	if err != nil {
		return tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Failed to initialize provider",
			err.Error(),
		))
	}

	addr := addrs.AbsProviderConfig{
		Module:   addrs.RootModule,
		Provider: providerType,
		Alias:    providerConfig.Alias,
	}

	configBody := buildRunbookProviderConfig(ctx, addr, providerConfig)
	_, noConfigDiags := configBody.Content(&hcl.BodySchema{})
	if !noConfigDiags.HasErrors() {
		return nil
	}

	schemaResp := provider.GetProviderSchema()
	var diags tfdiags.Diagnostics
	diags = diags.Append(schemaResp.Diagnostics.InConfigBody(configBody, addr.String()))
	if diags.HasErrors() {
		return diags
	}

	configSchema := schemaResp.Provider.Body
	if configSchema == nil {
		configSchema = &configschema.Block{}
	}

	configVal, _, evalDiags := ctx.EvaluateBlock(configBody, configSchema)
	diags = diags.Append(evalDiags)
	if diags.HasErrors() {
		return diags
	}

	unmarkedConfigVal, _ := configVal.UnmarkDeep()
	validateResp := provider.ValidateProviderConfig(providers.ValidateProviderConfigRequest{
		Config: unmarkedConfigVal,
	})
	diags = diags.Append(validateResp.Diagnostics.InConfigBody(configBody, addr.String()))

	return diags
}

func providerTypeForConfig(config *runbookconfigs.RunbookConfig, providerConfig *configs.Provider) addrs.Provider {
	if config != nil && config.ProviderRequirements != nil {
		if req, ok := config.ProviderRequirements.RequiredProviders[providerConfig.Name]; ok {
			return req.Type
		}
	}
	return addrs.ImpliedProviderForUnqualifiedType(providerConfig.Name)
}

func providerFactory(providerType addrs.Provider, opts *ValidateOpts) providers.Factory {
	if opts == nil || opts.Providers == nil {
		return nil
	}
	return opts.Providers[providerType]
}

func buildRunbookProviderConfig(ctx ProviderValidationContext, addr addrs.AbsProviderConfig, config *configs.Provider) hcl.Body {
	var configBody hcl.Body
	if config != nil {
		configBody = config.Config
	}

	var inputBody hcl.Body
	inputConfig := ctx.ProviderInput(addr)
	if len(inputConfig) > 0 {
		inputBody = configs.SynthBody("<input-prompt>", inputConfig)
	}

	switch {
	case configBody != nil && inputBody != nil:
		return hcl.MergeBodies([]hcl.Body{inputBody, configBody})
	case configBody != nil:
		return configBody
	case inputBody != nil:
		return inputBody
	default:
		return hcl.EmptyBody()
	}
}

var _ lang.Data = providerEvalData{}

type providerEvalData struct {
	ctx *EvalContext
}

func (providerEvalData) StaticValidateReferences(refs []*addrs.Reference, self addrs.Referenceable, source addrs.Referenceable) tfdiags.Diagnostics {
	return nil
}
func (providerEvalData) GetCountAttr(addrs.CountAttr, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.NilVal, nil
}
func (providerEvalData) GetForEachAttr(addrs.ForEachAttr, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.NilVal, nil
}
func (providerEvalData) GetResource(addrs.Resource, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}
func (providerEvalData) GetLocalValue(addrs.LocalValue, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}
func (providerEvalData) GetModule(addrs.ModuleCall, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}
func (providerEvalData) GetPathAttr(addrs.PathAttr, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}
func (providerEvalData) GetTerraformAttr(addrs.TerraformAttr, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}
func (d providerEvalData) GetInputVariable(addr addrs.InputVariable, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	if d.ctx != nil {
		if value, ok := d.ctx.GetVariable(addr.Name); ok && value != nil {
			return value.Value, nil
		}
		if d.ctx.config != nil {
			if variable, ok := d.ctx.config.Variables[addr.Name]; ok {
				if variable.Default != cty.NilVal {
					return variable.Default, nil
				}
				if variable.Type != cty.NilType {
					return cty.UnknownVal(variable.Type), nil
				}
			}
		}
	}
	return cty.DynamicVal, nil
}
func (providerEvalData) GetOutput(addrs.OutputValue, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}
func (providerEvalData) GetCheckBlock(addrs.Check, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}
func (providerEvalData) GetRunBlock(addrs.Run, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	return cty.DynamicVal, nil
}
