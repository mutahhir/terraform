// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"github.com/hashicorp/hcl/v2"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// ParseWorkspaceReference parses a workspace-scoped reference.
//
// Valid examples include:
//   - workspace.output.result
//   - workspace.action.http.notify
//   - workspace.action.http.notify["blue"]
//   - workspace.module.child.action.http.notify
//   - workspace.module.child["blue"].action.http.notify
func ParseWorkspaceReference(traversal hcl.Traversal) (Referenceable, hcl.Range, hcl.Traversal, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	if traversal.RootName() != "workspace" {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid workspace reference",
			Detail:   "Workspace references must begin with the keyword \"workspace\".",
			Subject:  traversal[0].SourceRange().Ptr(),
		})
		return nil, hcl.Range{}, nil, diags
	}

	if len(traversal) < 2 {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid workspace reference",
			Detail:   "The \"workspace\" object cannot be accessed directly. Instead, access an output or action in the workspace, such as \"workspace.output.<name>\", \"workspace.action.<type>.<name>\", or \"workspace.module.<name>.action.<type>.<name>\".",
			Subject:  traversal.SourceRange().Ptr(),
		})
		return nil, hcl.Range{}, nil, diags
	}

	trimmed := hcl.Traversal(traversal[1:])

	var next string
	switch tt := trimmed[0].(type) {
	case hcl.TraverseRoot:
		next = tt.Name
	case hcl.TraverseAttr:
		next = tt.Name
	default:
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid workspace reference",
			Detail:   "After \"workspace\", the reference must continue with an attribute access.",
			Subject:  trimmed[0].SourceRange().Ptr(),
		})
		return nil, hcl.Range{}, nil, diags
	}

	switch next {
	case "output":
		for i := len(trimmed); i > 0; i-- {
			prefix := trimmed[:i]
			addr, moreDiags := addrs.ParseAbsOutputValue(prefix)
			if moreDiags.HasErrors() {
				continue
			}

			target := WorkspaceOutputValue{Output: addr}
			rng := hcl.RangeBetween(traversal[0].SourceRange(), prefix[len(prefix)-1].SourceRange())
			return target, rng, trimmed[i:], diags
		}

		_, moreDiags := addrs.ParseAbsOutputValue(trimmed)
		diags = diags.Append(moreDiags)
		return nil, hcl.Range{}, nil, diags
	case "action", "module":
		for i := len(trimmed); i > 0; i-- {
			prefix := trimmed[:i]
			addr, moreDiags := addrs.ParseAbsActionInstance(prefix)
			if moreDiags.HasErrors() {
				continue
			}

			var target Referenceable
			if addr.Action.Key == addrs.NoKey {
				target = WorkspaceAction{Action: addr.ContainingAction()}
			} else {
				target = WorkspaceActionInstance{Action: addr}
			}

			rng := hcl.RangeBetween(traversal[0].SourceRange(), prefix[len(prefix)-1].SourceRange())
			return target, rng, trimmed[i:], diags
		}

		_, moreDiags := addrs.ParseAbsActionInstance(trimmed)
		diags = diags.Append(moreDiags)
		return nil, hcl.Range{}, nil, diags
	default:
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid workspace reference",
			Detail:   "After \"workspace\", the reference must begin with either \"output\" or \"action\" to access a workspace output or action, or \"module\" to access an action inside a child module.",
			Subject:  trimmed[0].SourceRange().Ptr(),
		})
		return nil, hcl.Range{}, nil, diags
	}
}

// ParseWorkspaceActionReference is retained as a compatibility wrapper for the
// action-only workspace parser name.
func ParseWorkspaceActionReference(traversal hcl.Traversal) (Referenceable, hcl.Range, hcl.Traversal, tfdiags.Diagnostics) {
	return ParseWorkspaceReference(traversal)
}
