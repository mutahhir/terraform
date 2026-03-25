// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookplan

import (
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/zclconf/go-cty/cty"
)

func StepOutputsFromQueries(step *runbookconfig.Step, queries []runbookconfig.PlannedQuery, vars cty.Value, steps cty.Value, workspace cty.Value) cty.Value {
	return StepOutputsFromQueriesWithScope(step, queries, runbookconfig.EvalScope{
		Variables: vars,
		Steps:     steps,
		Workspace: workspace,
	})
}

func StepOutputsFromQueriesWithScope(step *runbookconfig.Step, queries []runbookconfig.PlannedQuery, scope runbookconfig.EvalScope) cty.Value {
	listScope := listScopeFromQueries(step, queries)
	if listScope == cty.NilVal {
		return cty.EmptyObjectVal
	}
	scope.List = listScope
	return evaluateInstanceOutputs(step, scope)
}
