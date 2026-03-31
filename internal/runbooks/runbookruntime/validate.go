// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

func (c *RunbookContext) Validate() tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if c == nil || c.config == nil {
		return diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Invalid runbook context",
			"A runbook runtime context requires a non-nil runbook configuration.",
		))
	}

	for _, step := range c.config.Steps {
		for _, execution := range step.Executions {
			for _, action := range execution.InvokeAction {
				diags = diags.Append(c.validateExecutableAction(step.Name, action))
			}
		}

		for _, output := range step.Outputs {
			diags = diags.Append(c.validateScopedExpressions(step.Name, output.Expr))
		}

		for _, condition := range step.Preconditions {
			diags = diags.Append(c.validateScopedExpressions(step.Name, condition.Condition, condition.ErrorMessage))
		}
		for _, condition := range step.Postconditions {
			diags = diags.Append(c.validateScopedExpressions(step.Name, condition.Condition, condition.ErrorMessage))
		}
	}

	for _, output := range c.config.Outputs {
		diags = diags.Append(c.validateScopedExpressions("", output.Expr))
	}

	diags = diags.Append(c.validateStepDependencyCycles())

	return diags
}

func (c *RunbookContext) validateScopedExpressions(currentStepName string, exprs ...hcl.Expression) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	for _, expr := range exprs {
		diags = diags.Append(c.validateExpressionRunbookScopeReferences(expr))
		diags = diags.Append(c.validateExpressionStepExternalReferences(currentStepName, expr))
		diags = diags.Append(c.validateExpressionWorkspaceReferences(expr))
		diags = diags.Append(c.validateExpressionStepLocalReferences(currentStepName, expr))
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
		if currentStepName != "" && cfgAddr.Step.Name != currentStepName {
			if from, ok := c.stepVertices[currentStepName]; ok {
				if to, ok := c.stepVertices[cfgAddr.Step.Name]; ok {
					c.stepDependencyGraph.Connect(dag.BasicEdge(from, to))
				}
			}
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

func (c *RunbookContext) validateExpressionWorkspaceReferences(expr hcl.Expression) tfdiags.Diagnostics {
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

func (c *RunbookContext) validateStepDependencyCycles() tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if c == nil || c.stepDependencyGraph == nil {
		return diags
	}
	for _, cycle := range c.stepDependencyGraph.Cycles() {
		cycleNames := make([]string, 0, len(cycle))
		for _, raw := range cycle {
			if step, ok := raw.(stepVertex); ok {
				cycleNames = append(cycleNames, step.Name())
			}
		}
		if len(cycleNames) == 0 {
			continue
		}
		diags = diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			fmt.Sprintf("Cycle: %s", strings.Join(cycleNames, ", ")),
			"",
		))
	}

	return diags
}
