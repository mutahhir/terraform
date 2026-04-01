// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookgraph"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type repetitionValidationScope struct {
	countAvailable bool
	eachAvailable  bool
}

func (c *RunbookContext) Validate() tfdiags.Diagnostics {
	graph, diags := (&RunbookPlanGraphBuilder{
		Context:   c,
		Operation: runbookgraph.WalkValidate,
	}).Build()
	if diags.HasErrors() {
		return diags
	}
	if graph != nil {
		diags = diags.Append(runbookgraph.Walk(graph, newRunbookGraphWalker(c, graph, runbookgraph.WalkValidate)))
	}
	return diags
}

func (c *RunbookContext) validateStepShell(step *Step) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if c == nil || step == nil || step.Config() == nil {
		return diags
	}
	stepCfg := step.Config()

	diags = diags.Append(c.validateScopedExpressions(stepCfg.Name, repetitionValidationScope{}, stepCfg.Count, stepCfg.ForEach))

	return diags
}

func (c *RunbookContext) validateStepLocal(stepName string, local *configs.Local) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if c == nil || local == nil {
		return diags
	}
	return diags.Append(c.validateScopedExpressions(stepName, repetitionValidationScope{}, local.Expr))
}

func (c *RunbookContext) validateStepAction(stepName string, action *configs.Action) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if c == nil || action == nil {
		return diags
	}
	diags = diags.Append(c.validateScopedExpressions(stepName, repetitionValidationScope{}, action.Count, action.ForEach))
	scope := repetitionValidationScope{countAvailable: action.Count != nil, eachAvailable: action.ForEach != nil}
	diags = diags.Append(c.validateBodyExpressions(stepName, scope, action.Config))
	return diags
}

func (c *RunbookContext) validateStepDataSource(stepName string, dataSource *configs.Resource) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if c == nil || dataSource == nil {
		return diags
	}
	diags = diags.Append(c.validateScopedExpressions(stepName, repetitionValidationScope{}, dataSource.Count, dataSource.ForEach))
	scope := repetitionValidationScope{countAvailable: dataSource.Count != nil, eachAvailable: dataSource.ForEach != nil}
	diags = diags.Append(c.validateBodyExpressions(stepName, scope, dataSource.Config))
	return diags
}

func (c *RunbookContext) validateStepList(stepName string, list *configs.Resource) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if c == nil || list == nil {
		return diags
	}
	diags = diags.Append(c.validateScopedExpressions(stepName, repetitionValidationScope{}, list.Count, list.ForEach))
	scope := repetitionValidationScope{countAvailable: list.Count != nil, eachAvailable: list.ForEach != nil}
	if list.List != nil {
		diags = diags.Append(c.validateScopedExpressions(stepName, scope, list.List.IncludeResource, list.List.Limit))
	}
	diags = diags.Append(c.validateBodyExpressions(stepName, scope, list.Config))
	return diags
}

func (c *RunbookContext) validateStepOutputValue(stepName string, output *configs.Output) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if c == nil || output == nil {
		return diags
	}
	return diags.Append(c.validateScopedExpressions(stepName, repetitionValidationScope{}, output.Expr))
}

func (c *RunbookContext) validateRunbookOutput(outputName string) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if c == nil || c.config == nil {
		return diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Invalid runbook context",
			"A runbook runtime context requires a non-nil runbook configuration.",
		))
	}
	output := c.config.Outputs[outputName]
	if output == nil {
		return diags
	}

	diags = diags.Append(c.validateScopedExpressions("", repetitionValidationScope{}, output.Expr))

	return diags
}

func (c *RunbookContext) validateScopedExpressions(currentStepName string, scope repetitionValidationScope, exprs ...hcl.Expression) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	for _, expr := range exprs {
		diags = diags.Append(c.validateExpressionRunbookScopeReferences(expr))
		diags = diags.Append(c.validateExpressionStepExternalReferences(currentStepName, expr))
		diags = diags.Append(c.validateExpressionWorkspaceReferences(currentStepName, expr))
		diags = diags.Append(c.validateExpressionRepetitionReferences(scope, expr))
		diags = diags.Append(c.validateExpressionStepLocalReferences(currentStepName, expr))
	}
	return diags
}

func (c *RunbookContext) validateBodyExpressions(currentStepName string, scope repetitionValidationScope, body hcl.Body) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if body == nil {
		return diags
	}

	syntaxBody, ok := body.(*hclsyntax.Body)
	if !ok {
		return diags
	}

	for _, attr := range syntaxBody.Attributes {
		diags = diags.Append(c.validateScopedExpressions(currentStepName, scope, attr.Expr))
	}
	for _, block := range syntaxBody.Blocks {
		diags = diags.Append(c.validateBodyExpressions(currentStepName, scope, block.Body))
	}

	return diags
}

func (c *RunbookContext) validateExpressionRepetitionReferences(scope repetitionValidationScope, expr hcl.Expression) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if expr == nil {
		return diags
	}

	for _, traversal := range expr.Variables() {
		rootName := traversal.RootName()
		if rootName != "count" && rootName != "each" {
			continue
		}

		ref, refDiags := runbookaddrs.ParseScopedReference(traversal)
		diags = diags.Append(refDiags)
		if refDiags.HasErrors() {
			continue
		}

		switch addr := ref.Target.(type) {
		case addrs.CountAttr:
			if addr.Name != "index" {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  `Invalid "count" attribute`,
					Detail:   fmt.Sprintf(`The "count" object does not have an attribute named %q. The only supported attribute is count.index.`, addr.Name),
					Subject:  traversal.SourceRange().Ptr(),
				})
				continue
			}
			if !scope.countAvailable {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  `Reference to "count" in non-counted context`,
					Detail:   `The "count" object can be used only in step-contained blocks when the enclosing block has the "count" argument set.`,
					Subject:  traversal.SourceRange().Ptr(),
				})
			}
		case addrs.ForEachAttr:
			if addr.Name != "key" && addr.Name != "value" {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  `Invalid "each" attribute`,
					Detail:   fmt.Sprintf(`The "each" object does not have an attribute named %q. The supported attributes are each.key and each.value.`, addr.Name),
					Subject:  traversal.SourceRange().Ptr(),
				})
				continue
			}
			if !scope.eachAvailable {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  `Reference to "each" in context without for_each`,
					Detail:   `The "each" object can be used only in step-contained blocks when the enclosing block has the "for_each" argument set.`,
					Subject:  traversal.SourceRange().Ptr(),
				})
			}
		}
	}

	return diags
}

func (c *RunbookContext) validateExecutableAction(stepName string, action runbookaddrs.ExecutableAction) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics

	switch addr := action.(type) {
	case runbookaddrs.ActionInstance:
		if c.StepAction(stepName, addr.Type, addr.Name) == nil {
			return diags.Append(tfdiags.Sourceless(
				tfdiags.Error,
				"Reference to undeclared action",
				fmt.Sprintf("The action %q is not declared in step %q.", addr.String(), stepName),
			))
		}
	case runbookaddrs.WorkspaceActionInstance:
		if !c.workspaceActionExists(addr.Action) {
			return diags.Append(tfdiags.Sourceless(
				tfdiags.Error,
				"Reference to undeclared workspace action",
				fmt.Sprintf("The workspace action %q does not exist in the loaded Terraform configuration.", addr.String()),
			))
		}
	default:
		return diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Invalid execute target",
			fmt.Sprintf("%T is not a valid executable action target.", action),
		))
	}

	return diags
}

func (c *RunbookContext) workspaceActionExists(addr addrs.AbsAction) bool {
	if c == nil || c.config == nil || c.config.WorkspaceConfig == nil {
		return false
	}

	moduleCfg := c.config.WorkspaceConfig.DescendantForInstance(addr.Module)
	if moduleCfg == nil || moduleCfg.Module == nil {
		return false
	}
	_, exists := moduleCfg.Module.Actions[addr.Action.String()]
	return exists
}

func (c *RunbookContext) validateExpressionRunbookScopeReferences(expr hcl.Expression) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if expr == nil {
		return diags
	}

	for _, traversal := range expr.Variables() {
		rootName := traversal.RootName()
		if rootName != "var" {
			continue
		}

		ref, refDiags := runbookaddrs.ParseRunbookReference(traversal)
		diags = diags.Append(refDiags)
		if refDiags.HasErrors() {
			continue
		}
		if addr, ok := ref.Target.(addrs.InputVariable); ok {
			if c.Variable(addr.Name) == nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Reference to undeclared variable",
					Detail:   fmt.Sprintf("The variable %q is not declared in the runbook configuration.", addr.Name),
					Subject:  traversal.SourceRange().Ptr(),
				})
			}
		}
	}

	return diags
}

func (c *RunbookContext) validateExpressionStepExternalReferences(currentStepName string, expr hcl.Expression) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if expr == nil {
		return diags
	}

	for _, traversal := range expr.Variables() {
		if traversal.RootName() != "step" {
			continue
		}

		ref, _, refDiags := runbookaddrs.ParseStepOutputReference(traversal)
		diags = diags.Append(refDiags)
		if refDiags.HasErrors() {
			continue
		}

		addr, ok := ref.Target.(runbookaddrs.StepOutputValue)
		if !ok {
			continue
		}
		cfgAddr := addr.ConfigStepOutputValue()
		if step := c.Step(cfgAddr.Step.Name); step != nil {
			diags = diags.Append(c.validateStepInstanceReference(step, addr, traversal))
		}
		if currentStepName != "" && cfgAddr.Step.Name == currentStepName {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid self-reference to step output",
				Detail:   fmt.Sprintf("Step %q cannot reference its own output %q. Reference the underlying values directly instead.", currentStepName, cfgAddr.Name),
				Subject:  traversal.SourceRange().Ptr(),
			})
			continue
		}
		if c.StepOutput(cfgAddr.Step.Name, cfgAddr.Name) == nil {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Reference to undeclared step output",
				Detail:   fmt.Sprintf("The step output %q does not exist in the loaded runbook configuration.", addr.String()),
				Subject:  traversal.SourceRange().Ptr(),
			})
		}
	}

	return diags
}

func (c *RunbookContext) validateStepInstanceReference(step *Step, addr runbookaddrs.StepOutputValue, traversal hcl.Traversal) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if step == nil || step.Config() == nil {
		return diags
	}
	stepCfg := step.Config()

	hasRepetition := stepCfg.Count != nil || stepCfg.ForEach != nil
	key := addr.Step.Key

	if key == addrs.WildcardKey {
		return diags
	}

	if key == addrs.NoKey {
		if hasRepetition {
			var detail string
			if stepCfg.Count != nil {
				detail = fmt.Sprintf("Because %s has \"count\" set, its outputs must be accessed on specific instances.\n\nFor example:\n    step.%s[count.index].%s", addr.Step.Step.String(), stepCfg.Name, addr.Name)
			} else {
				detail = fmt.Sprintf("Because %s has \"for_each\" set, its outputs must be accessed on specific instances.\n\nFor example:\n    step.%s[each.key].%s", addr.Step.Step.String(), stepCfg.Name, addr.Name)
			}
			return diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Missing step instance key",
				Detail:   detail,
				Subject:  traversal.SourceRange().Ptr(),
			})
		}
		return diags
	}

	if !hasRepetition {
		return diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Unexpected step instance key",
			Detail:   fmt.Sprintf("Because %s does not have \"count\" or \"for_each\" set, references to it must not include an index key.", addr.Step.Step.String()),
			Subject:  traversal.SourceRange().Ptr(),
		})
	}

	switch key.(type) {
	case addrs.IntKey:
		if stepCfg.Count == nil {
			return diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid step instance key",
				Detail:   fmt.Sprintf("Step %q uses \"for_each\", so references to its instances must use string keys, not numeric indexes.", stepCfg.Name),
				Subject:  traversal.SourceRange().Ptr(),
			})
		}
	case addrs.StringKey:
		if stepCfg.ForEach == nil {
			return diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid step instance key",
				Detail:   fmt.Sprintf("Step %q uses \"count\", so references to its instances must use numeric indexes, not string keys.", stepCfg.Name),
				Subject:  traversal.SourceRange().Ptr(),
			})
		}
	}

	return diags
}

func (c *RunbookContext) validateExpressionWorkspaceReferences(currentStepName string, expr hcl.Expression) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if expr == nil {
		return diags
	}

	for _, traversal := range expr.Variables() {
		if traversal.RootName() != "workspace" {
			continue
		}

		target, rng, _, refDiags := runbookaddrs.ParseWorkspaceReference(traversal)
		diags = diags.Append(refDiags)
		if refDiags.HasErrors() {
			continue
		}

		_ = rng
		if addr, ok := target.(runbookaddrs.WorkspaceOutputValue); ok {
			if !c.workspaceOutputExists(addr) {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Reference to undeclared workspace output",
					Detail:   fmt.Sprintf("The workspace output %q does not exist in the loaded Terraform configuration.", addr.String()),
					Subject:  traversal.SourceRange().Ptr(),
				})
				continue
			}
			c.usedWorkspaceOutputNames = append(c.usedWorkspaceOutputNames, addr)
			if currentStepName != "" {
				c.workspaceOutputsByStep[currentStepName] = append(c.workspaceOutputsByStep[currentStepName], addr)
			}
		}
	}

	return diags
}

func (c *RunbookContext) workspaceOutputExists(addr runbookaddrs.WorkspaceOutputValue) bool {
	if c == nil || c.config == nil || c.config.WorkspaceConfig == nil {
		return false
	}

	if !addr.Output.Module.IsRoot() {
		return false
	}
	_, exists := c.config.WorkspaceConfig.Module.Outputs[addr.Output.OutputValue.Name]
	return exists
}

func (c *RunbookContext) validateExpressionStepLocalReferences(currentStepName string, expr hcl.Expression) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if expr == nil {
		return diags
	}

	for _, traversal := range expr.Variables() {
		rootName := traversal.RootName()
		if rootName == "step" || rootName == "workspace" || rootName == "var" {
			continue
		}
		if len(traversal) == 1 {
			continue
		}

		ref, refDiags := runbookaddrs.ParseInStepReference(traversal)
		diags = diags.Append(refDiags)
		if refDiags.HasErrors() {
			continue
		}

		switch addr := ref.Target.(type) {
		case addrs.LocalValue:
			if currentStepName == "" || c.StepLocal(currentStepName, addr.Name) == nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Reference to undeclared local value",
					Detail:   fmt.Sprintf("The local value %q is not declared in step %q.", addr.Name, currentStepName),
					Subject:  traversal.SourceRange().Ptr(),
				})
			}
		case runbookaddrs.ActionInstance:
			if len(ref.Remaining) == 0 {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid action reference",
					Detail:   "Action values are not directly referenceable in expressions. Use an explicit action attribute such as \".output\".",
					Subject:  traversal.SourceRange().Ptr(),
				})
				continue
			}
			firstStep, ok := ref.Remaining[0].(hcl.TraverseAttr)
			if !ok || firstStep.Name != "output" {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid action reference",
					Detail:   "Action expressions must access an explicit action attribute, currently \".output\".",
					Subject:  traversal.SourceRange().Ptr(),
				})
				continue
			}
			if currentStepName == "" || c.StepAction(currentStepName, addr.Type, addr.Name) == nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Reference to undeclared action",
					Detail:   fmt.Sprintf("The action %q is not declared in step %q.", addr.String(), currentStepName),
					Subject:  traversal.SourceRange().Ptr(),
				})
			}
		case runbookaddrs.DataSource:
			if currentStepName == "" || c.StepDataSource(currentStepName, addr.Type, addr.Name) == nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Reference to undeclared data source",
					Detail:   fmt.Sprintf("The data source %q is not declared in step %q.", addr.String(), currentStepName),
					Subject:  traversal.SourceRange().Ptr(),
				})
			}
		case runbookaddrs.List:
			if currentStepName == "" || c.StepList(currentStepName, addr.Type, addr.Name) == nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Reference to undeclared list block",
					Detail:   fmt.Sprintf("The list block %q is not declared in step %q.", addr.String(), currentStepName),
					Subject:  traversal.SourceRange().Ptr(),
				})
			}
		}
	}

	return diags
}
