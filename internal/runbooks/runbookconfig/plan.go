// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"fmt"
	"strings"

	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

type PlannedAction struct {
	Address    string
	ActionType string
	ActionName string
}

type PlannedQuery struct {
	Address string
	Count   int
	Data    cty.Value
}

type StepPlan struct {
	Evaluation StepEvaluation
	Actions    []PlannedAction
	Queries    []PlannedQuery
	Lowered    *LoweredStepBundle
}

func PlanStep(step *Step, scope EvalScope) StepPlan {
	return planStep(nil, step, scope)
}

func PlanStepWithConfig(cfg *Config, step *Step, scope EvalScope) StepPlan {
	return planStep(cfg, step, scope)
}

func planStep(cfg *Config, step *Step, scope EvalScope) StepPlan {
	eval := EvaluateStepForPlan(step, scope)
	plan := StepPlan{Evaluation: eval}
	if step == nil || eval.Status == StepStatusFailed || eval.Status == StepStatusSkipped {
		return plan
	}

	actionsByRef := make(map[string]*Action)
	if cfg != nil {
		workspaceActions, _ := WorkspaceActions(cfg)
		for ref, action := range workspaceActions {
			if action != nil {
				actionsByRef[ref] = action
			}
		}
		for ref, action := range cfg.Actions {
			if action != nil {
				actionsByRef[ref] = action
			}
		}
	}
	for _, action := range step.Actions {
		if action == nil {
			continue
		}
		actionsByRef[action.Reference()] = action
	}

	for _, invoke := range step.ExecuteInvokes {
		if invoke == nil {
			continue
		}
		action, ok := actionsByRef[invoke.ActionRef]
		if !ok {
			plan.Evaluation.Status = StepStatusFailed
			plan.Evaluation.Diags = plan.Evaluation.Diags.Append(tfdiags.Sourceless(
				tfdiags.Error,
				"Unknown action reference",
				fmt.Sprintf("Execute block references %q, but no matching action block exists in the runbook or this step.", invoke.ActionRef),
			))
			continue
		}
		plan.Actions = append(plan.Actions, PlannedAction{
			Address:    invoke.ActionRef,
			ActionType: action.Type,
			ActionName: action.Name,
		})
	}

	if len(plan.Actions) > 0 && plan.Evaluation.Detail == "conditions satisfied" {
		var actionAddrs []string
		for _, action := range plan.Actions {
			actionAddrs = append(actionAddrs, action.Address)
		}
		plan.Evaluation.Detail = fmt.Sprintf("will invoke %s", strings.Join(actionAddrs, ", "))
	}

	lowered, diags := LowerStep(cfg, step)
	plan.Lowered = lowered
	plan.Evaluation.Diags = plan.Evaluation.Diags.Append(diags)

	return plan
}
