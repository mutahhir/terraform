package runbookconfigs

import (
	"github.com/hashicorp/hcl/v2"
)

type Execution struct {
	InvokeAction []hcl.Traversal
}

func decodeExecutionBlock(block *hcl.Block) (*Execution, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	exec := &Execution{
		InvokeAction: make([]hcl.Traversal, 0),
	}

	content, moreDiags := block.Body.Content(executeSchema)
	diags = append(diags, moreDiags...)
	if len(content.Blocks) == 0 {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Empty execute block",
			Detail:   "An execute block must contain at least one invoke_action block.",
			Subject:  block.DefRange.Ptr(),
		})
		return nil, diags
	}

	for _, innerBlock := range content.Blocks {
		switch innerBlock.Type {
		case "invoke_action":
			invokeContent, invokeDiags := innerBlock.Body.Content(invokeActionSchema)
			diags = append(diags, invokeDiags...)

			attr := invokeContent.Attributes["action"]
			if attr == nil {
				continue
			}

			traversal, traversalDiags := hcl.AbsTraversalForExpr(attr.Expr)
			diags = append(diags, traversalDiags...)
			if traversalDiags.HasErrors() {
				continue
			}

			exec.InvokeAction = append(exec.InvokeAction, traversal)
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
