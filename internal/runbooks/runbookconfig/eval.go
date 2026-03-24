// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/lang"
	"github.com/zclconf/go-cty/cty"

	"github.com/hashicorp/terraform/internal/tfdiags"
)

type EvalScope struct {
	Variables     cty.Value
	List          cty.Value
	Actions       cty.Value
	Steps         cty.Value
	Locals        cty.Value
	Count         cty.Value
	Each          cty.Value
	Workspace     cty.Value
	ExternalFuncs lang.ExternalFuncs
}

type StepStatus string

const (
	StepStatusReady     StepStatus = "ready"
	StepStatusSkipped   StepStatus = "skipped"
	StepStatusFailed    StepStatus = "failed"
	StepStatusSucceeded StepStatus = "succeeded"
)

type StepEvaluation struct {
	Status StepStatus
	Detail string
	Diags  tfdiags.Diagnostics
}

func EvaluateStepForPlan(step *Step, scope EvalScope) StepEvaluation {
	if step == nil {
		return StepEvaluation{
			Status: StepStatusFailed,
			Detail: "step not found",
		}
	}
	scope = ScopeWithStepLists(step, scope)
	return evaluateConditions(step.Preconditions, scope, true)
}

func EvaluateStepForExecution(step *Step, preScope, postScope EvalScope) StepEvaluation {
	if step == nil {
		return StepEvaluation{
			Status: StepStatusFailed,
			Detail: "step not found",
		}
	}
	preScope = ScopeWithStepLists(step, preScope)
	postScope = ScopeWithStepLists(step, postScope)

	pre := evaluateConditions(step.Preconditions, preScope, true)
	if pre.Status == StepStatusSkipped || pre.Status == StepStatusFailed {
		return pre
	}

	post := evaluateConditions(step.Postconditions, postScope, false)
	if post.Status == StepStatusFailed {
		return post
	}

	return StepEvaluation{
		Status: StepStatusSucceeded,
		Detail: "step execution conditions satisfied",
	}
}

func evaluateConditions(conditions []*Condition, scope EvalScope, allowSkip bool) StepEvaluation {
	if len(conditions) == 0 {
		return StepEvaluation{
			Status: StepStatusReady,
			Detail: "no conditions",
		}
	}

	for _, condition := range conditions {
		result, diags := evaluateCondition(condition, scope)
		if diags.HasErrors() {
			return StepEvaluation{
				Status: StepStatusFailed,
				Detail: "condition evaluation failed",
				Diags:  diags,
			}
		}
		if result {
			continue
		}

		if allowSkip && condition.OnFail == ConditionOnFailSkip {
			return StepEvaluation{
				Status: StepStatusSkipped,
				Detail: "step skipped by precondition",
				Diags:  conditionFailureDiagnostics(condition, scope),
			}
		}

		return StepEvaluation{
			Status: StepStatusFailed,
			Detail: "step condition failed",
			Diags:  conditionFailureDiagnostics(condition, scope),
		}
	}

	return StepEvaluation{
		Status: StepStatusReady,
		Detail: "conditions satisfied",
	}
}

func evaluateCondition(condition *Condition, scope EvalScope) (bool, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if condition == nil || condition.Condition == nil {
		return true, diags
	}
	resultVal, evalDiags := EvalExpr(condition.Condition, scope, cty.Bool)
	diags = diags.Append(evalDiags)
	if evalDiags.HasErrors() {
		return false, diags
	}
	if !resultVal.IsKnown() || resultVal.IsNull() {
		return false, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("Unknown %s result", condition.Kind),
			Detail:   fmt.Sprintf("The %s condition must evaluate to a known boolean value.", condition.Kind),
			Subject:  condition.Condition.Range().Ptr(),
		})
	}

	return resultVal.True(), diags
}

func conditionFailureDiagnostics(condition *Condition, scope EvalScope) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if condition == nil || condition.ErrorMessage == nil {
		return diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Runbook condition failed",
			"A step condition returned false.",
		))
	}

	val, evalDiags := EvalExpr(condition.ErrorMessage, scope, cty.String)
	diags = diags.Append(evalDiags)
	if evalDiags.HasErrors() {
		return diags
	}
	if !val.IsKnown() || val.IsNull() {
		return diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Runbook condition failed",
			"A step condition returned false.",
		))
	}

	return diags.Append(tfdiags.Sourceless(
		tfdiags.Error,
		"Runbook condition failed",
		val.AsString(),
	))
}

func normalizeScopeValue(v cty.Value) cty.Value {
	if v == cty.NilVal {
		return cty.EmptyObjectVal
	}
	return v
}
