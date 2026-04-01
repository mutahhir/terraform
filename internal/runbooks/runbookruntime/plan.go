// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"maps"

	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

type PlanOpts struct {
	PlanTimeInputs *PlanTimeInputs
}

type Plan struct {
	Graph *PlanGraph
}

func (c *RunbookContext) Plan(opts *PlanOpts) (*Plan, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if c == nil {
		return nil, diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Invalid runbook plan",
			"A runbook plan requires a non-nil runtime context.",
		))
	}

	originalInputs := c.planTimeInputs
	if opts != nil && opts.PlanTimeInputs != nil {
		c.planTimeInputs = PlanTimeInputs{
			Variables:        map[string]cty.Value{},
			WorkspaceOutputs: map[string]cty.Value{},
		}
		maps.Copy(c.planTimeInputs.Variables, opts.PlanTimeInputs.Variables)
		maps.Copy(c.planTimeInputs.WorkspaceOutputs, opts.PlanTimeInputs.WorkspaceOutputs)
		defer func() {
			c.planTimeInputs = originalInputs
		}()
	}

	builder := &RunbookPlanGraphBuilder{
		Context:   c,
		Opts:      opts,
		Operation: walkPlan,
	}
	graph, moreDiags := builder.Build()
	diags = diags.Append(moreDiags)
	if diags.HasErrors() {
		return nil, diags
	}
	diags = diags.Append(graph.Walk(newRunbookGraphWalker(c, graph, walkPlan)))
	if diags.HasErrors() {
		return nil, diags
	}

	return &Plan{Graph: graph}, diags
}
