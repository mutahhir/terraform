// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"

	"github.com/hashicorp/terraform/internal/configs"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// validateStepReferences checks that all inter-step references in the runbook
// config resolve to declared steps. This catches references to non-existent
// steps early (at validation time) rather than producing confusing eval
// failures during the graph walk.
//
// It also validates that referenced step outputs are declared on the target
// step when the reference includes a specific output name.
func validateStepReferences(config *runbookconfigs.RunbookConfig) tfdiags.Diagnostics {
	if config == nil || len(config.Steps) == 0 {
		return nil
	}

	// Build the set of declared step names
	declaredSteps := make(map[string]struct{}, len(config.Steps))
	for name := range config.Steps {
		declaredSteps[name] = struct{}{}
	}

	// Build a set of declared outputs per step for output-level validation
	declaredOutputs := make(map[string]map[string]struct{}, len(config.Steps))
	for name, step := range config.Steps {
		outputs := make(map[string]struct{}, len(step.Outputs))
		for _, output := range step.Outputs {
			if output != nil {
				outputs[output.Name] = struct{}{}
			}
		}
		declaredOutputs[name] = outputs
	}

	var diags tfdiags.Diagnostics

	for stepName, step := range config.Steps {
		if step == nil {
			continue
		}

		refs := referencesForStep(step)
		traversals := stepReferenceTraversals(step)

		for i, ref := range refs {
			var targetStepName string
			var targetOutputName string

			switch r := ref.(type) {
			case runbookaddrs.StepOutput:
				targetStepName = r.Step.StepName
				targetOutputName = r.OutputName
			case runbookaddrs.Step:
				targetStepName = r.Step.StepName
			default:
				continue
			}

			// Skip self-references and empty step names (intra-step refs)
			if targetStepName == "" || targetStepName == stepName {
				continue
			}

			// Find the source range for this reference
			var subject *hcl.Range
			if i < len(traversals) && traversals[i] != nil {
				rng := traversals[i].SourceRange()
				subject = &rng
			}

			// Check the target step exists
			if _, exists := declaredSteps[targetStepName]; !exists {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Reference to undeclared step",
					Detail:   fmt.Sprintf("Step %q references step %q, which is not declared in this runbook.", stepName, targetStepName),
					Subject:  subject,
				})
				continue
			}

			// If the reference targets a specific output, check it exists
			if targetOutputName != "" {
				if outputs, ok := declaredOutputs[targetStepName]; ok {
					if _, exists := outputs[targetOutputName]; !exists {
						diags = diags.Append(&hcl.Diagnostic{
							Severity: hcl.DiagError,
							Summary:  "Reference to undeclared step output",
							Detail:   fmt.Sprintf("Step %q references output %q on step %q, but that step does not declare an output with that name.", stepName, targetOutputName, targetStepName),
							Subject:  subject,
						})
					}
				}
			}
		}
	}

	return diags
}

// stepReferenceTraversals collects the HCL traversals that correspond to the
// references returned by referencesForStep, maintaining index alignment so
// we can provide source ranges for diagnostics.
func stepReferenceTraversals(step *runbookconfigs.Step) []hcl.Traversal {
	if step == nil {
		return nil
	}

	var traversals []hcl.Traversal
	traversals = append(traversals, stepTraversalsFromExpr(step.Count)...)
	traversals = append(traversals, stepTraversalsFromExpr(step.ForEach)...)
	for _, local := range step.Locals {
		if local != nil {
			traversals = append(traversals, stepTraversalsFromExpr(local.Expr)...)
		}
	}
	for _, action := range step.Actions {
		traversals = append(traversals, stepTraversalsForAction(action)...)
	}
	for _, data := range step.DataSources {
		traversals = append(traversals, stepTraversalsForResource(data)...)
	}
	for _, list := range step.ListResources {
		traversals = append(traversals, stepTraversalsForResource(list)...)
	}
	for _, execution := range step.Executions {
		traversals = append(traversals, stepTraversalsForExecution(execution)...)
	}
	for _, condition := range step.Preconditions {
		traversals = append(traversals, stepTraversalsForCondition(condition)...)
	}
	for _, condition := range step.Postconditions {
		traversals = append(traversals, stepTraversalsForCondition(condition)...)
	}
	for _, output := range step.Outputs {
		traversals = append(traversals, stepTraversalsForOutput(output)...)
	}
	return traversals
}

func stepTraversalsFromExpr(expr hcl.Expression) []hcl.Traversal {
	if expr == nil {
		return nil
	}
	variables := expr.Variables()
	traversals := make([]hcl.Traversal, 0, len(variables))
	for _, traversal := range variables {
		ref, diags := runbookaddrs.ParseRef(traversal)
		if diags.HasErrors() || ref == nil {
			continue
		}
		switch ref.Subject.(type) {
		case runbookaddrs.StepOutput, runbookaddrs.Step:
			traversals = append(traversals, traversal)
		}
	}
	return traversals
}

func stepTraversalsForAction(action *configs.Action) []hcl.Traversal {
	if action == nil {
		return nil
	}
	var traversals []hcl.Traversal
	traversals = append(traversals, stepTraversalsFromExpr(action.Count)...)
	traversals = append(traversals, stepTraversalsFromExpr(action.ForEach)...)
	if action.Config != nil {
		traversals = append(traversals, stepTraversalsFromBody(action.Config)...)
	}
	return traversals
}

func stepTraversalsForResource(resource *configs.Resource) []hcl.Traversal {
	if resource == nil {
		return nil
	}
	var traversals []hcl.Traversal
	traversals = append(traversals, stepTraversalsFromExpr(resource.Count)...)
	traversals = append(traversals, stepTraversalsFromExpr(resource.ForEach)...)
	if resource.Config != nil {
		traversals = append(traversals, stepTraversalsFromBody(resource.Config)...)
	}
	if resource.List != nil {
		traversals = append(traversals, stepTraversalsFromExpr(resource.List.IncludeResource)...)
		traversals = append(traversals, stepTraversalsFromExpr(resource.List.Limit)...)
	}
	return traversals
}

func stepTraversalsForExecution(execution *runbookconfigs.Execution) []hcl.Traversal {
	if execution == nil {
		return nil
	}
	var traversals []hcl.Traversal
	for _, t := range execution.InvokeAction {
		ref, diags := runbookaddrs.ParseRef(t)
		if diags.HasErrors() || ref == nil {
			continue
		}
		switch ref.Subject.(type) {
		case runbookaddrs.StepOutput, runbookaddrs.Step:
			traversals = append(traversals, t)
		}
	}
	for _, t := range execution.ReadDataSources {
		ref, diags := runbookaddrs.ParseRef(t)
		if diags.HasErrors() || ref == nil {
			continue
		}
		switch ref.Subject.(type) {
		case runbookaddrs.StepOutput, runbookaddrs.Step:
			traversals = append(traversals, t)
		}
	}
	return traversals
}

func stepTraversalsForCondition(condition *runbookconfigs.Condition) []hcl.Traversal {
	if condition == nil {
		return nil
	}
	var traversals []hcl.Traversal
	traversals = append(traversals, stepTraversalsFromExpr(condition.Condition)...)
	traversals = append(traversals, stepTraversalsFromExpr(condition.ErrorMessage)...)
	return traversals
}

func stepTraversalsForOutput(output *configs.Output) []hcl.Traversal {
	if output == nil {
		return nil
	}
	var traversals []hcl.Traversal
	traversals = append(traversals, stepTraversalsFromExpr(output.Expr)...)
	for _, traversal := range output.DependsOn {
		ref, diags := runbookaddrs.ParseRef(traversal)
		if diags.HasErrors() || ref == nil {
			continue
		}
		switch ref.Subject.(type) {
		case runbookaddrs.StepOutput, runbookaddrs.Step:
			traversals = append(traversals, traversal)
		}
	}
	for _, condition := range output.Preconditions {
		traversals = append(traversals, stepTraversalsFromExpr(condition.Condition)...)
		traversals = append(traversals, stepTraversalsFromExpr(condition.ErrorMessage)...)
	}
	return traversals
}

func stepTraversalsFromBody(body hcl.Body) []hcl.Traversal {
	if body == nil {
		return nil
	}
	var traversals []hcl.Traversal
	attrs, _ := body.JustAttributes()
	for _, attr := range attrs {
		traversals = append(traversals, stepTraversalsFromExpr(attr.Expr)...)
	}
	content, _, _ := body.PartialContent(&hcl.BodySchema{})
	for _, block := range content.Blocks {
		traversals = append(traversals, stepTraversalsFromBody(block.Body)...)
	}
	return traversals
}
