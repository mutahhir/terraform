// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// Reference describes a reference expression that can appear anywhere within a
// runbook configuration.
//
// The target can be any address kind that is meaningful to the runbook
// language, including runbook variables, step-local objects, cross-step output
// references, workspace-scoped objects, and repetition context objects.
type Reference struct {
	Target      any
	SourceRange tfdiags.SourceRange
	Remaining   hcl.Traversal
}

// ParseReference raises a raw traversal into a higher-level runbook-specific
// reference, or returns diagnostics explaining why it cannot.
func ParseReference(traversal hcl.Traversal) (Reference, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	var ret Reference

	switch traversal.RootName() {
	case "step":
		target, rng, remain, moreDiags := parseStepOutputRef(traversal)
		diags = diags.Append(moreDiags)
		if diags.HasErrors() {
			return ret, diags
		}
		ret.Target = target
		ret.SourceRange = tfdiags.SourceRangeFromHCL(rng)
		ret.Remaining = remain
		return ret, diags
	case "workspace":
		target, rng, remain, moreDiags := ParseWorkspaceReference(traversal)
		diags = diags.Append(moreDiags)
		if diags.HasErrors() {
			return ret, diags
		}
		ret.Target = target
		ret.SourceRange = tfdiags.SourceRangeFromHCL(rng)
		ret.Remaining = remain
		return ret, diags
	default:
		ref, moreDiags := parseScopedReference(traversal)
		diags = diags.Append(moreDiags)
		if diags.HasErrors() {
			return ret, diags
		}
		return ref, diags
	}
}

func parseScopedReference(traversal hcl.Traversal) (Reference, tfdiags.Diagnostics) {
	var ret Reference

	ref, diags := addrs.ParseRef(traversal)
	if diags.HasErrors() {
		return ret, diags
	}
	if ref == nil {
		return ret, diags
	}

	if moreDiags := validateInStepReference(ref); moreDiags.HasErrors() {
		return ret, diags.Append(moreDiags)
	}

	ret.Target = normalizeInStepReferenceTarget(ref.Subject)
	ret.SourceRange = ref.SourceRange
	ret.Remaining = ref.Remaining
	return ret, diags
}

// ParseRunbookReference parses a traversal as a runbook-scoped reference.
//
// For now this is limited to references like `var.*`.
func ParseRunbookReference(traversal hcl.Traversal) (Reference, tfdiags.Diagnostics) {
	ret, diags := ParseReference(traversal)
	if diags.HasErrors() {
		return ret, diags
	}

	if _, ok := ret.Target.(addrs.InputVariable); ok {
		return ret, diags
	}

	var moreDiags tfdiags.Diagnostics
	moreDiags = moreDiags.Append(&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Invalid runbook reference",
		Detail:   "Only runbook-scoped variables can be referenced from the runbook scope.",
		Subject:  traversal.SourceRange().Ptr(),
	})
	return Reference{}, diags.Append(moreDiags)
}

// ParseStepOutputReference parses a traversal as a cross-step step output
// reference.
func ParseStepOutputReference(traversal hcl.Traversal) (Reference, tfdiags.Diagnostics) {
	ret, diags := ParseReference(traversal)
	if diags.HasErrors() {
		return ret, diags
	}

	if _, ok := ret.Target.(StepOutputValue); ok {
		return ret, diags
	}

	var moreDiags tfdiags.Diagnostics
	moreDiags = moreDiags.Append(&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Reference to unknown symbol",
		Detail:   fmt.Sprintf("There is no symbol %q defined in the current scope.", traversal.RootName()),
		Subject:  traversal[0].SourceRange().Ptr(),
	})
	return Reference{}, diags.Append(moreDiags)
}

// ParseInStepReference parses a traversal as a reference resolved from the
// current step's own local scope.
func ParseInStepReference(traversal hcl.Traversal) (Reference, tfdiags.Diagnostics) {
	ret, diags := ParseReference(traversal)
	if diags.HasErrors() {
		return ret, diags
	}

	if _, ok := ret.Target.(addrs.InputVariable); ok {
		var moreDiags tfdiags.Diagnostics
		moreDiags = moreDiags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid in-step reference",
			Detail:   "Runbook variables are not step-local references. Handle them through the runbook scope instead.",
			Subject:  traversal.SourceRange().Ptr(),
		})
		return Reference{}, diags.Append(moreDiags)
	}

	switch ret.Target.(type) {
	case ActionInstance, DataSource, List, WorkspaceActionInstance, WorkspaceOutputValue, StepOutputValue:
		return ret, diags
	}
	if _, ok := ret.Target.(addrs.LocalValue); ok {
		return ret, diags
	}
	if _, ok := ret.Target.(addrs.CountAttr); ok {
		return ret, diags
	}
	if _, ok := ret.Target.(addrs.ForEachAttr); ok {
		return ret, diags
	}
	if ret.Target == addrs.Self {
		return ret, diags
	}

	var moreDiags tfdiags.Diagnostics
	moreDiags = moreDiags.Append(&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Invalid in-step reference",
		Detail:   fmt.Sprintf("References to %T are not valid inside a runbook step.", ret.Target),
		Subject:  traversal.SourceRange().Ptr(),
	})
	return Reference{}, diags.Append(moreDiags)
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
