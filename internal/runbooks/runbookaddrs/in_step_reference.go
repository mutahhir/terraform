// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// ScopedReference describes a reference expression found inside a step body
// that is not step-external (`step.*`) or workspace-scoped (`workspace.*`).
//
// Unlike [CrossStepReference], this parser is for symbols resolved within the
// current step's evaluation scope or the enclosing runbook scope, such as data
// sources, list blocks, actions, locals, variables, and contextual references
// like count.index.
//
// This parser reuses Terraform's low-level reference parsing, but then narrows
// the resulting targets down to only the address kinds that can actually exist
// in a runbook step.
type ScopedReference struct {
	Target      any
	SourceRange tfdiags.SourceRange
	Remaining   hcl.Traversal
}

// ParseScopedReference parses a traversal as a reference resolved from the
// current runbook/step scope, excluding step-external and workspace-scoped
// references.
func ParseScopedReference(traversal hcl.Traversal) (ScopedReference, tfdiags.Diagnostics) {
	var ret ScopedReference

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

// RunbookReference describes a reference expression resolved from the enclosing
// runbook scope.
type RunbookReference = ScopedReference

// ParseRunbookReference parses a traversal as a runbook-scoped reference.
//
// For now this is limited to references like `var.*`.
func ParseRunbookReference(traversal hcl.Traversal) (RunbookReference, tfdiags.Diagnostics) {
	ret, diags := ParseScopedReference(traversal)
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
	return RunbookReference{}, diags.Append(moreDiags)
}

// InStepReference describes a reference expression resolved from the current
// step's own local scope.
type InStepReference = ScopedReference

// ParseInStepReference parses a traversal as a step-local reference.
func ParseInStepReference(traversal hcl.Traversal) (InStepReference, tfdiags.Diagnostics) {
	ret, diags := ParseScopedReference(traversal)
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
		return InStepReference{}, diags.Append(moreDiags)
	}

	return ret, diags
}

func normalizeInStepReferenceTarget(subject addrs.Referenceable) any {
	switch addr := subject.(type) {
	case addrs.Resource:
		switch addr.Mode {
		case addrs.DataResourceMode:
			return DataSource{Type: addr.Type, Name: addr.Name}
		case addrs.ListResourceMode:
			return List{Type: addr.Type, Name: addr.Name}
		}
	case addrs.ResourceInstance:
		switch addr.Resource.Mode {
		case addrs.DataResourceMode:
			return DataSource{
				Type: addr.Resource.Type,
				Name: addr.Resource.Name,
				Key:  addr.Key,
			}
		case addrs.ListResourceMode:
			return List{
				Type: addr.Resource.Type,
				Name: addr.Resource.Name,
				Key:  addr.Key,
			}
		}
	case addrs.Action:
		return ActionInstance{Type: addr.Type, Name: addr.Name}
	case addrs.ActionInstance:
		return ActionInstance{
			Type: addr.Action.Type,
			Name: addr.Action.Name,
			Key:  addr.Key,
		}
	}

	return subject
}

func validateInStepReference(ref *addrs.Reference) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics

	var summary, detail string
	subjectRange := ref.SourceRange.ToHCL().Ptr()

	switch subject := ref.Subject.(type) {
	case addrs.Resource:
		return validateInStepResourceMode(subject.Mode, subjectRange)
	case addrs.ResourceInstance:
		return validateInStepResourceMode(subject.Resource.Mode, subjectRange)
	}

	if ref.Subject == addrs.Self {
		return diags
	}

	switch ref.Subject.(type) {
	case addrs.Action,
		addrs.ActionInstance,
		addrs.Resource,
		addrs.ResourceInstance,
		addrs.LocalValue,
		addrs.InputVariable,
		addrs.CountAttr,
		addrs.ForEachAttr:
		return diags

	case addrs.ModuleCall, addrs.ModuleCallInstance, addrs.ModuleCallInstanceOutput:
		summary = "Invalid in-step reference"
		detail = "Runbook steps do not contain module calls. References inside a step can refer only to objects declared in that step."

	case addrs.OutputValue:
		summary = "Invalid in-step reference"
		detail = "Runbook step outputs are not addressed using the \"output\" object inside the step. Reference the underlying values directly, or use \"step.<name>.<output>\" for cross-step references."

	case addrs.PathAttr:
		summary = "Invalid in-step reference"
		detail = "Runbook steps do not support the \"path\" object. References inside a step can refer only to step-local objects."

	case addrs.TerraformAttr:
		summary = "Invalid in-step reference"
		detail = "Runbook steps do not support the \"terraform\" object. References inside a step can refer only to step-local objects."

	case addrs.Check:
		summary = "Invalid in-step reference"
		detail = "Runbook steps do not contain check blocks. References inside a step can refer only to objects declared in that step."

	case addrs.Run:
		summary = "Invalid in-step reference"
		detail = "Runbook steps do not contain run blocks. References inside a step can refer only to objects declared in that step."

	default:
		summary = "Invalid in-step reference"
		detail = fmt.Sprintf("References to %T are not valid inside a runbook step.", ref.Subject)
	}

	return diags.Append(&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   detail,
		Subject:  subjectRange,
	})
}

func validateInStepResourceMode(mode addrs.ResourceMode, subjectRange *hcl.Range) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics

	switch mode {
	case addrs.DataResourceMode, addrs.ListResourceMode:
		return diags
	case addrs.ManagedResourceMode:
		return diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid in-step reference",
			Detail:   "Runbook steps do not contain managed resources. References inside a step can refer to data sources, list blocks, actions, locals, variables, and step instance context only.",
			Subject:  subjectRange,
		})
	case addrs.EphemeralResourceMode:
		return diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid in-step reference",
			Detail:   "Runbook steps do not contain ephemeral resources. References inside a step can refer to data sources, list blocks, actions, locals, variables, and step instance context only.",
			Subject:  subjectRange,
		})
	default:
		return diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid in-step reference",
			Detail:   "This resource type is not valid inside a runbook step.",
			Subject:  subjectRange,
		})
	}
}
