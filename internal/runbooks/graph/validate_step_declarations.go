package runbookgraph

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/lang"
	"github.com/hashicorp/terraform/internal/lang/langrefs"
	"github.com/hashicorp/terraform/internal/providers"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

func validateStepDeclarations(config *runbookconfigs.RunbookConfig, ctx *BuiltinEvalContext, opts *ValidateOpts) tfdiags.Diagnostics {
	if config == nil {
		return nil
	}

	cache := newProviderSchemaCache(config, opts)
	var diags tfdiags.Diagnostics
	diags = diags.Append(validateReadDataSourceRefs(config))
	diags = diags.Append(validateNoDynamicDataInExpansion(config))
	for _, step := range sortSteps(config.Steps) {
		diags = diags.Append(validateStepActionDeclarations(config, ctx, cache, step))
		diags = diags.Append(validateStepDataDeclarations(config, ctx, cache, step))
		diags = diags.Append(validateStepListDeclarations(config, ctx, cache, step))
		diags = diags.Append(validateStepWaitDeclarations(step))
		for _, local := range step.Locals {
			diags = diags.Append(validateWorkspaceReferencesInExpr(config, cache, local.Expr))
		}
		for _, output := range step.Outputs {
			diags = diags.Append(validateWorkspaceReferencesInExpr(config, cache, output.Expr))
		}
		for _, condition := range step.Preconditions {
			diags = diags.Append(validateWorkspaceReferencesInExpr(config, cache, condition.Condition))
		}
		for _, condition := range step.Postconditions {
			diags = diags.Append(validateWorkspaceReferencesInExpr(config, cache, condition.Condition))
		}
	}
	return diags
}

func validateStepActionDeclarations(config *runbookconfigs.RunbookConfig, ctx *BuiltinEvalContext, cache *providerSchemaCache, step *runbookconfigs.Step) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	for _, action := range step.Actions {
		diags = diags.Append(validateWorkspaceReferencesInBody(config, cache, action.Config))
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

func validateStepDataDeclarations(config *runbookconfigs.RunbookConfig, ctx *BuiltinEvalContext, cache *providerSchemaCache, step *runbookconfigs.Step) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	for _, data := range step.DataSources {
		diags = diags.Append(validateWorkspaceReferencesInBody(config, cache, data.Config))
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

func validateStepListDeclarations(config *runbookconfigs.RunbookConfig, ctx *BuiltinEvalContext, cache *providerSchemaCache, step *runbookconfigs.Step) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	for _, list := range step.ListResources {
		diags = diags.Append(validateWorkspaceReferencesInBody(config, cache, list.Config))
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
	case "each", "count", "local", "data", "list", "step", "action", "workspace":
		return true
	default:
		return false
	}
}

func validateWorkspaceReferencesInBody(config *runbookconfigs.RunbookConfig, cache *providerSchemaCache, body hcl.Body) tfdiags.Diagnostics {
	if body == nil {
		return nil
	}
	attrs, _ := body.JustAttributes()
	var diags tfdiags.Diagnostics
	for _, attr := range attrs {
		diags = diags.Append(validateWorkspaceReferencesInExpr(config, cache, attr.Expr))
	}
	content, _, _ := body.PartialContent(&hcl.BodySchema{})
	for _, block := range content.Blocks {
		diags = diags.Append(validateWorkspaceReferencesInBody(config, cache, block.Body))
	}
	return diags
}

func validateWorkspaceReferencesInExpr(config *runbookconfigs.RunbookConfig, cache *providerSchemaCache, expr hcl.Expression) tfdiags.Diagnostics {
	if expr == nil {
		return nil
	}
	var diags tfdiags.Diagnostics
	for _, traversal := range expr.Variables() {
		root, ok := traversal[0].(hcl.TraverseRoot)
		if ok && root.Name != "workspace" {
			continue
		}
		ref, refDiags := runbookaddrs.ParseRef(traversal)
		diags = diags.Append(refDiags)
		if refDiags.HasErrors() || ref == nil {
			continue
		}
		resourceRef, ok := ref.Subject.(runbookaddrs.WorkspaceResource)
		if !ok {
			continue
		}
		diags = diags.Append(validateWorkspaceResourceReference(config, cache, ref, resourceRef))
	}
	return diags
}

func validateWorkspaceResourceReference(config *runbookconfigs.RunbookConfig, cache *providerSchemaCache, ref *runbookaddrs.Reference, resourceRef runbookaddrs.WorkspaceResource) tfdiags.Diagnostics {
	resourceConfig := workspaceResourceConfig(config, resourceRef)
	if resourceConfig == nil {
		return tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Unknown workspace object",
			Detail:   fmt.Sprintf("The workspace reference %s does not match any resource or data source in the workspace configuration.", workspaceRefString(ref)),
			Subject:  ref.SourceRange.ToHCL().Ptr(),
		})
	}
	_, schemaResp, diags := resourceProviderSchema(config, cache, resourceConfig)
	if diags.HasErrors() {
		return diags
	}
	resourceSchema := schemaResp.SchemaForResourceAddr(resourceConfig.Addr())
	if resourceSchema.Body == nil {
		return nil
	}
	return validateWorkspaceTraversalAgainstSchema(ref, resourceSchema.Body)
}

func validateWorkspaceTraversalAgainstSchema(ref *runbookaddrs.Reference, schema *configschema.Block) tfdiags.Diagnostics {
	if ref == nil || len(ref.Remaining) == 0 || schema == nil {
		return nil
	}
	var diags tfdiags.Diagnostics
	if hclDiags := schema.StaticValidateTraversal(ref.Remaining); hclDiags.HasErrors() {
		return diags.Append(hclDiags)
	}
	current := schema
	for _, step := range ref.Remaining {
		attr, ok := step.(hcl.TraverseAttr)
		if !ok {
			continue
		}
		attribute := current.Attributes[attr.Name]
		if attribute == nil {
			continue
		}
		if attribute.Sensitive {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Sensitive workspace attribute",
				Detail:   fmt.Sprintf("The workspace reference %s targets sensitive attribute %q, which cannot be used in a runbook.", workspaceRefString(ref), attr.Name),
				Subject:  step.SourceRange().Ptr(),
			})
		}
		if attribute.WriteOnly {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Write-only workspace attribute",
				Detail:   fmt.Sprintf("The workspace reference %s targets write-only attribute %q, which is not available from workspace state.", workspaceRefString(ref), attr.Name),
				Subject:  step.SourceRange().Ptr(),
			})
		}
		if attribute.NestedType != nil {
			current = &configschema.Block{Attributes: attribute.NestedType.Attributes}
		}
	}
	return diags
}

func workspaceResourceConfig(config *runbookconfigs.RunbookConfig, ref runbookaddrs.WorkspaceResource) *configs.Resource {
	if config == nil || config.WorkspaceConfig == nil {
		return nil
	}
	target := config.WorkspaceConfig
	for _, call := range ref.Module.Calls {
		child, ok := target.Children[call.Name]
		if !ok || child == nil {
			return nil
		}
		target = child
	}
	if target.Module == nil {
		return nil
	}
	resources := target.Module.ManagedResources
	if ref.Resource.Mode == terraformaddrs.DataResourceMode {
		resources = target.Module.DataResources
	}
	for _, resource := range resources {
		if resource != nil && resource.Type == ref.Resource.Type && resource.Name == ref.Resource.Name {
			return resource
		}
	}
	return nil
}

func workspaceRefString(ref *runbookaddrs.Reference) string {
	if ref == nil || ref.Subject == nil {
		return "workspace"
	}
	var b strings.Builder
	b.WriteString(ref.Subject.String())
	for _, step := range ref.Remaining {
		switch s := step.(type) {
		case hcl.TraverseAttr:
			b.WriteByte('.')
			b.WriteString(s.Name)
		case hcl.TraverseIndex:
			b.WriteString(tfdiags.TraversalStr(hcl.Traversal{step}))
		}
	}
	return b.String()
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

func evaluateExpr(ctx *BuiltinEvalContext, expr hcl.Expression) (cty.Value, tfdiags.Diagnostics) {
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

// validateStepWaitDeclarations checks that wait operations within a step's
// execute blocks reference valid data sources declared in the same step.
func validateStepWaitDeclarations(step *runbookconfigs.Step) tfdiags.Diagnostics {
	if step == nil {
		return nil
	}

	// Collect data source names declared in this step
	declaredData := make(map[string]bool)
	for _, data := range step.DataSources {
		declaredData[fmt.Sprintf("data.%s.%s", data.Type, data.Name)] = true
	}

	var diags tfdiags.Diagnostics
	for _, exec := range step.Executions {
		for _, op := range exec.Operations {
			if op.Type != runbookconfigs.ExecuteOpWait || op.Wait == nil {
				continue
			}
			wait := op.Wait

			// Validate datasource references a data block in this step
			if wait.Mode == runbookconfigs.WaitModePolling && wait.DataSource != nil {
				dsRef := wait.DataSource.RootName()
				if dsRef == "data" && len(wait.DataSource) >= 3 {
					// Build the full data reference: data.<type>.<name>
					typePart := ""
					namePart := ""
					if attr, ok := wait.DataSource[1].(hcl.TraverseAttr); ok {
						typePart = attr.Name
					}
					if attr, ok := wait.DataSource[2].(hcl.TraverseAttr); ok {
						namePart = attr.Name
					}
					fullRef := fmt.Sprintf("data.%s.%s", typePart, namePart)
					if typePart != "" && namePart != "" && !declaredData[fullRef] {
						diags = diags.Append(tfdiags.Sourceless(
							tfdiags.Error,
							"Wait datasource not found in step",
							fmt.Sprintf("The wait %q references %s, but this data source is not declared in step %q.", wait.Name, fullRef, step.Name),
						))
					}
				} else if dsRef != "data" {
					diags = diags.Append(tfdiags.Sourceless(
						tfdiags.Error,
						"Invalid wait datasource reference",
						fmt.Sprintf("The wait %q datasource must reference a data source (e.g. data.type.name), got root %q.", wait.Name, dsRef),
					))
				}
			}
		}
	}
	return diags
}
