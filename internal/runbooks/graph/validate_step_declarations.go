package runbookgraph

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/lang"
	"github.com/hashicorp/terraform/internal/lang/langrefs"
	"github.com/hashicorp/terraform/internal/providers"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

func validateStepDeclarations(config *runbookconfigs.RunbookConfig, ctx *EvalContext, opts *ValidateOpts) tfdiags.Diagnostics {
	if config == nil {
		return nil
	}

	cache := newProviderSchemaCache(config, opts)
	var diags tfdiags.Diagnostics
	for _, step := range sortSteps(config.Steps) {
		diags = diags.Append(validateStepActionDeclarations(config, ctx, cache, step))
		diags = diags.Append(validateStepDataDeclarations(config, ctx, cache, step))
		diags = diags.Append(validateStepListDeclarations(config, ctx, cache, step))
	}
	return diags
}

func validateStepActionDeclarations(config *runbookconfigs.RunbookConfig, ctx *EvalContext, cache *providerSchemaCache, step *runbookconfigs.Step) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	for _, action := range step.Actions {
		provider, schema, actionDiags := actionProviderSchema(config, cache, action)
		diags = diags.Append(actionDiags)
		if actionDiags.HasErrors() {
			continue
		}

		actionSchema := schema.SchemaForActionType(action.Type)
		if actionSchema.ConfigSchema == nil {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid action type",
				Detail:   fmt.Sprintf("The provider %s does not support action type %q.", providerTypeForAction(config, action).ForDisplay(), action.Type),
				Subject:  &action.TypeRange,
			})
			continue
		}

		configBody := action.Config
		if configBody == nil {
			configBody = hcl.EmptyBody()
		}
		if hasRunbookReferencesInBody(configBody) {
			continue
		}
		configVal, _, bodyDiags := ctx.EvaluateBlock(configBody, actionSchema.ConfigSchema)
		diags = diags.Append(bodyDiags.InConfigBody(configBody, action.Type))
		if bodyDiags.HasErrors() {
			continue
		}

		resp := provider.ValidateActionConfig(providers.ValidateActionConfigRequest{
			TypeName: action.Type,
			Config:   configVal,
		})
		diags = diags.Append(resp.Diagnostics.InConfigBody(configBody, action.Type))
	}
	return diags
}

func validateStepDataDeclarations(config *runbookconfigs.RunbookConfig, ctx *EvalContext, cache *providerSchemaCache, step *runbookconfigs.Step) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	for _, data := range step.DataSources {
		provider, schema, dataDiags := resourceProviderSchema(config, cache, data)
		diags = diags.Append(dataDiags)
		if dataDiags.HasErrors() {
			continue
		}

		resourceSchema := schema.SchemaForResourceAddr(data.Addr())
		if resourceSchema.Body == nil {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid data source",
				Detail:   fmt.Sprintf("The provider %s does not support data source %q.", providerTypeForResource(config, data).ForDisplay(), data.Type),
				Subject:  &data.TypeRange,
			})
			continue
		}

		configBody := data.Config
		if configBody == nil {
			configBody = hcl.EmptyBody()
		}
		if hasRunbookReferencesInBody(configBody) {
			continue
		}
		configVal, _, bodyDiags := ctx.EvaluateBlock(configBody, resourceSchema.Body)
		diags = diags.Append(bodyDiags.InConfigBody(configBody, data.Addr().String()))
		if bodyDiags.HasErrors() {
			continue
		}

		resp := provider.ValidateDataResourceConfig(providers.ValidateDataResourceConfigRequest{
			TypeName: data.Type,
			Config:   configVal,
		})
		diags = diags.Append(resp.Diagnostics.InConfigBody(data.Config, data.Addr().String()))
	}
	return diags
}

func validateStepListDeclarations(config *runbookconfigs.RunbookConfig, ctx *EvalContext, cache *providerSchemaCache, step *runbookconfigs.Step) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	for _, list := range step.ListResources {
		provider, schema, listDiags := resourceProviderSchema(config, cache, list)
		diags = diags.Append(listDiags)
		if listDiags.HasErrors() {
			continue
		}

		listSchema := schema.SchemaForListResourceType(list.Type)
		if listSchema.IsNil() {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid list resource",
				Detail:   fmt.Sprintf("The provider %s does not support list resource %q.", providerTypeForResource(config, list).ForDisplay(), list.Type),
				Subject:  &list.TypeRange,
			})
			continue
		}

		var blockVal cty.Value
		configBody := list.Config
		if configBody == nil {
			configBody = hcl.EmptyBody()
		}
		if hasRunbookReferencesInBody(configBody) {
			continue
		}
		blockVal, _, listDiags = ctx.EvaluateBlock(configBody, listSchema.FullSchema)
		diags = diags.Append(listDiags.InConfigBody(configBody, list.Addr().String()))
		if listDiags.HasErrors() {
			continue
		}

		limit := cty.NilVal
		if list.List != nil && list.List.Limit != nil {
			var limitDiags tfdiags.Diagnostics
			limit, limitDiags = evaluateExpr(ctx, list.List.Limit)
			diags = diags.Append(limitDiags)
			if limitDiags.HasErrors() {
				continue
			}
		}

		includeResource := cty.NilVal
		if list.List != nil && list.List.IncludeResource != nil {
			var includeDiags tfdiags.Diagnostics
			includeResource, includeDiags = evaluateExpr(ctx, list.List.IncludeResource)
			diags = diags.Append(includeDiags)
			if includeDiags.HasErrors() {
				continue
			}
		}

		unmarkedBlockVal, _ := blockVal.UnmarkDeep()
		if !unmarkedBlockVal.IsNull() && listSchema.ConfigSchema != nil && unmarkedBlockVal.Type().HasAttribute("config") && unmarkedBlockVal.GetAttr("config").IsNull() {
			mp := unmarkedBlockVal.AsValueMap()
			mp["config"] = listSchema.ConfigSchema.EmptyValue()
			unmarkedBlockVal = cty.ObjectVal(mp)
		}

		resp := provider.ValidateListResourceConfig(providers.ValidateListResourceConfigRequest{
			TypeName:              list.Type,
			Config:                unmarkedBlockVal,
			Limit:                 limit,
			IncludeResourceObject: includeResource,
		})
		diags = diags.Append(resp.Diagnostics.InConfigBody(list.Config, list.Addr().String()))
	}
	return diags
}

type providerSchemaCache struct {
	config    *runbookconfigs.RunbookConfig
	providers map[terraformaddrs.Provider]providers.Factory
	schemas   map[terraformaddrs.Provider]providers.ProviderSchema
	clients   map[terraformaddrs.Provider]providers.Interface
}

func newProviderSchemaCache(config *runbookconfigs.RunbookConfig, opts *ValidateOpts) *providerSchemaCache {
	cache := &providerSchemaCache{
		config:    config,
		providers: map[terraformaddrs.Provider]providers.Factory{},
		schemas:   map[terraformaddrs.Provider]providers.ProviderSchema{},
		clients:   map[terraformaddrs.Provider]providers.Interface{},
	}
	if opts != nil && opts.Providers != nil {
		cache.providers = opts.Providers
	}
	return cache
}

func hasRunbookReferencesInBody(body hcl.Body) bool {
	if body == nil {
		return false
	}
	attrs, _ := body.JustAttributes()
	for _, attr := range attrs {
		for _, traversal := range attr.Expr.Variables() {
			if hasDeferredBodyReferenceTraversal(traversal) {
				return true
			}
		}
	}
	content, _, _ := body.PartialContent(&hcl.BodySchema{})
	for _, block := range content.Blocks {
		if hasRunbookReferencesInBody(block.Body) {
			return true
		}
	}
	return false
}

func hasDeferredBodyReferenceTraversal(traversal hcl.Traversal) bool {
	if len(traversal) == 0 {
		return false
	}
	root, ok := traversal[0].(hcl.TraverseRoot)
	if !ok {
		return false
	}
	switch root.Name {
	case "each", "count", "local", "data", "list", "step", "steps", "action", "workspace":
		return true
	default:
		return false
	}
}

func (c *providerSchemaCache) provider(providerType terraformaddrs.Provider) (providers.Interface, providers.ProviderSchema, tfdiags.Diagnostics) {
	return c.providerWithSubject(providerType, nil)
}

func (c *providerSchemaCache) providerWithSubject(providerType terraformaddrs.Provider, subject *hcl.Range) (providers.Interface, providers.ProviderSchema, tfdiags.Diagnostics) {
	if schema, ok := c.schemas[providerType]; ok {
		return c.clients[providerType], schema, nil
	}

	factory := c.providers[providerType]
	if factory == nil {
		return nil, providers.ProviderSchema{}, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Missing provider implementation",
			Detail:   fmt.Sprintf("No provider factory is configured for %s.", providerType),
			Subject:  subject,
		})
	}

	provider, err := factory()
	if err != nil {
		return nil, providers.ProviderSchema{}, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Failed to initialize provider",
			Detail:   err.Error(),
			Subject:  subject,
		})
	}

	schemaResp := provider.GetProviderSchema()
	if schemaResp.Diagnostics.HasErrors() {
		return nil, providers.ProviderSchema{}, schemaResp.Diagnostics
	}

	c.clients[providerType] = provider
	c.schemas[providerType] = schemaResp
	return provider, schemaResp, nil
}

func actionProviderSchema(config *runbookconfigs.RunbookConfig, cache *providerSchemaCache, action *configs.Action) (providers.Interface, providers.ProviderSchema, tfdiags.Diagnostics) {
	var subject *hcl.Range
	if action != nil {
		subject = &action.TypeRange
	}
	return cache.providerWithSubject(providerTypeForAction(config, action), subject)
}

func resourceProviderSchema(config *runbookconfigs.RunbookConfig, cache *providerSchemaCache, resource *configs.Resource) (providers.Interface, providers.ProviderSchema, tfdiags.Diagnostics) {
	var subject *hcl.Range
	if resource != nil {
		subject = &resource.TypeRange
	}
	return cache.providerWithSubject(providerTypeForResource(config, resource), subject)
}

func providerTypeForAction(config *runbookconfigs.RunbookConfig, action *configs.Action) terraformaddrs.Provider {
	if action != nil && action.Provider != (terraformaddrs.Provider{}) {
		return action.Provider
	}
	if action != nil {
		return providerTypeForLocalName(config, action.ProviderConfigAddr().LocalName)
	}
	return terraformaddrs.Provider{}
}

func providerTypeForResource(config *runbookconfigs.RunbookConfig, resource *configs.Resource) terraformaddrs.Provider {
	if resource != nil && resource.Provider != (terraformaddrs.Provider{}) {
		return resource.Provider
	}
	if resource != nil {
		return providerTypeForLocalName(config, resource.ProviderConfigAddr().LocalName)
	}
	return terraformaddrs.Provider{}
}

func providerTypeForLocalName(config *runbookconfigs.RunbookConfig, localName string) terraformaddrs.Provider {
	if config != nil && config.ProviderRequirements != nil {
		if req, ok := config.ProviderRequirements.RequiredProviders[localName]; ok {
			return req.Type
		}
	}
	return terraformaddrs.ImpliedProviderForUnqualifiedType(localName)
}

func evaluateExpr(ctx *EvalContext, expr hcl.Expression) (cty.Value, tfdiags.Diagnostics) {
	if expr == nil {
		return cty.NilVal, nil
	}

	refs, diags := langrefs.ReferencesInExpr(terraformaddrs.ParseRef, expr)
	scope := &lang.Scope{Data: providerEvalData{ctx: ctx}, ParseRef: terraformaddrs.ParseRef}
	hclCtx, ctxDiags := scope.EvalContext(refs)
	diags = diags.Append(ctxDiags)
	if diags.HasErrors() {
		return cty.DynamicVal, diags
	}

	value, hclDiags := expr.Value(hclCtx)
	return value, diags.Append(hclDiags)
}
