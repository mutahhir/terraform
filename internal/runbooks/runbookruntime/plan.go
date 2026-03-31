// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type Plan struct {
	Steps []*StepInstance
}

func (c *RunbookContext) BuildPlan() (*Plan, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if c == nil {
		return nil, diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Invalid runbook plan",
			"A runbook plan requires a non-nil runtime context.",
		))
	}

	orderedSteps := c.StepExecutionOrder()
	if orderedSteps == nil {
		return nil, diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Invalid runbook plan",
			"Cannot build a runbook plan while the step dependency graph contains cycles or unresolved vertices.",
		))
	}

	ret := &Plan{Steps: make([]*StepInstance, 0)}
	for _, step := range orderedSteps {
		instances, unknown, moreDiags := step.CheckInstances()
		diags = diags.Append(moreDiags)
		if moreDiags.HasErrors() {
			continue
		}
		if unknown {
			var subject *hcl.Range
			if cfg := step.Config(); cfg != nil {
				switch {
				case cfg.Count != nil:
					subject = cfg.Count.Range().Ptr()
				case cfg.ForEach != nil:
					subject = cfg.ForEach.Range().Ptr()
				}
			}
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Unknown step repetition during planning",
				Detail:   fmt.Sprintf("The repetition for step %q is not fully known at plan time. Runbooks require all step instances to be determined before execution planning.", step.Name()),
				Subject:  subject,
			})
			continue
		}

		keys := make([]addrs.InstanceKey, 0, len(instances))
		for key := range instances {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			return keys[i].String() < keys[j].String()
		})
		for _, key := range keys {
			ret.Steps = append(ret.Steps, instances[key])
		}
	}

	if diags.HasErrors() {
		return nil, diags
	}
	return ret, diags
}
