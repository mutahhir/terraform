// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// CrossStepReference describes a reference expression that crosses step
// boundaries in a runbook configuration, capturing what it referred to and
// where it was found in source code.
//
// This is intentionally narrower than all possible references that can appear
// within a step body. It models references resolved outside the current step,
// such as cross-step output references and workspace action references.
type CrossStepReference struct {
	Target      Referenceable
	SourceRange tfdiags.SourceRange
}

// ParseStepOutputReference raises a raw absolute traversal into a higher-level
// runbook-specific step output reference, or returns diagnostics explaining why
// it cannot.
//
// The returned traversal is a relative traversal covering the remainder of the
// given traversal after the part captured into the returned reference.
func ParseStepOutputReference(traversal hcl.Traversal) (CrossStepReference, hcl.Traversal, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	var ret CrossStepReference

	switch traversal.RootName() {
	case "step":
		target, rng, remain, moreDiags := parseStepOutputRef(traversal)
		diags = diags.Append(moreDiags)
		if diags.HasErrors() {
			return ret, nil, diags
		}
		ret.Target = target
		ret.SourceRange = tfdiags.SourceRangeFromHCL(rng)
		return ret, remain, diags
	default:
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Reference to unknown symbol",
			Detail:   fmt.Sprintf("There is no symbol %q defined in the current scope.", traversal.RootName()),
			Subject:  traversal[0].SourceRange().Ptr(),
		})
		return ret, nil, diags
	}
}
func parseStepOutputRef(traversal hcl.Traversal) (Referenceable, hcl.Range, hcl.Traversal, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	stepInst, remain, moreDiags := ParseAbsStepInstanceOnly(traversal)
	diags = diags.Append(moreDiags)
	if diags.HasErrors() {
		return nil, hcl.Range{}, nil, diags
	}

	if len(remain) == 0 {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid reference",
			Detail:   "The \"step\" object cannot be accessed directly. Instead, access one of the step's output values.",
			Subject:  traversal.SourceRange().Ptr(),
		})
		return nil, hcl.Range{}, nil, diags
	}

	outputStep, ok := remain[0].(hcl.TraverseAttr)
	if !ok {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid reference",
			Detail:   "The \"step\" object does not support this operation. Access one of the step's output values by name.",
			Subject:  remain[0].SourceRange().Ptr(),
		})
		return nil, hcl.Range{}, nil, diags
	}

	rng := hcl.RangeBetween(traversal[0].SourceRange(), outputStep.SourceRange())
	return StepOutputValue{
		Step: stepInst,
		Name: outputStep.Name,
	}, rng, remain[1:], diags
}
