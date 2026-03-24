// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

func Validate(cfg *Config) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if cfg == nil {
		return diags
	}
	if cfg.Runbook == nil {
		return diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Missing runbook block",
			"Runbook configuration must declare exactly one runbook block.",
		))
	}

	for _, file := range cfg.Files {
		for _, step := range file.Steps {
			if step.Count != nil && step.ForEach != nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid step repetition",
					Detail:   fmt.Sprintf("Step %q cannot use both count and for_each at the same time.", step.Name),
					Subject:  step.DeclRange.ToHCL().Ptr(),
				})
			}
			if step.ForEach != nil && step.ExecCount == 0 {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid for_each step",
					Detail:   fmt.Sprintf("Step %q uses for_each but has no execute block. Expanded steps must have execute behavior.", step.Name),
					Subject:  step.DeclRange.ToHCL().Ptr(),
				})
			}
			if step.Count != nil && step.ExecCount == 0 {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid count step",
					Detail:   fmt.Sprintf("Step %q uses count but has no execute block. Expanded steps must have execute behavior.", step.Name),
					Subject:  step.DeclRange.ToHCL().Ptr(),
				})
			}
			if len(step.Preconditions) > 0 && step.ExecCount == 0 {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid precondition block",
					Detail:   fmt.Sprintf("Step %q has preconditions but no execute block. Preconditions gate execute behavior.", step.Name),
					Subject:  step.DeclRange.ToHCL().Ptr(),
				})
			}
			if len(step.Postconditions) > 0 && step.ExecCount == 0 {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid postcondition block",
					Detail:   fmt.Sprintf("Step %q has postconditions but no execute block. Postconditions are evaluated after execute.", step.Name),
					Subject:  step.DeclRange.ToHCL().Ptr(),
				})
			}
		}
	}

	return diags
}
