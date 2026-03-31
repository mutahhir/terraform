package runbookconfig

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
)

type Execution struct {
	InvokeAction []runbookaddrs.ExecutableAction
}

func decodeExecutionBlock(block *hcl.Block) (*Execution, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	exec := &Execution{
		InvokeAction: make([]runbookaddrs.ExecutableAction, 0),
	}

	content, moreDiags := block.Body.Content(executeSchema)
	diags = append(diags, moreDiags...)

	for _, innerBlock := range content.Blocks {
		switch innerBlock.Type {
		case "invoke_action":
			invokeContent, invokeDiags := innerBlock.Body.Content(invokeActionSchema)
			diags = append(diags, invokeDiags...)

			attr := invokeContent.Attributes["action"]
			traversal, traversalDiags := hcl.AbsTraversalForExpr(attr.Expr)
			diags = append(diags, traversalDiags...)
			if traversalDiags.HasErrors() {
				continue
			}

			var action runbookaddrs.ExecutableAction
			switch traversal.RootName() {
			case "workspace":
				target, rng, remain, refDiags := runbookaddrs.ParseWorkspaceReference(traversal)
				diags = append(diags, refDiags.ToHCL()...)
				if refDiags.HasErrors() {
					continue
				}
				_ = rng
				if len(remain) > 0 {
					diags = append(diags, &hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Invalid invoke_action reference",
						Detail:   "The action reference must not include additional traversal after the action address.",
						Subject:  remain.SourceRange().Ptr(),
					})
					continue
				}

				var ok bool
				action, ok = target.(runbookaddrs.ExecutableAction)
				if !ok {
					diags = append(diags, &hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Invalid invoke_action reference",
						Detail:   "The action attribute must refer to a workspace action, not another kind of external reference.",
						Subject:  attr.Expr.Range().Ptr(),
					})
					continue
				}

			default:
				ref, refDiags := runbookaddrs.ParseInStepReference(traversal)
				diags = append(diags, refDiags.ToHCL()...)
				if refDiags.HasErrors() {
					continue
				}
				if len(ref.Remaining) > 0 {
					diags = append(diags, &hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Invalid invoke_action reference",
						Detail:   "The action reference must not include additional traversal after the action address.",
						Subject:  ref.Remaining.SourceRange().Ptr(),
					})
					continue
				}

				var ok bool
				action, ok = ref.Target.(runbookaddrs.ExecutableAction)
				if !ok {
					diags = append(diags, &hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Invalid invoke_action reference",
						Detail:   "The action attribute must refer to an action declared in the current step or in the workspace.",
						Subject:  attr.Expr.Range().Ptr(),
					})
					continue
				}
			}

			exec.InvokeAction = append(exec.InvokeAction, action)
		}
	}

	return exec, diags
}

var executeSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{
			Type: "invoke_action",
		},
	},
}
