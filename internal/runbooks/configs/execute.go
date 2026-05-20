package runbookconfigs

import (
	"github.com/hashicorp/hcl/v2"
)

// ExecuteOperationType identifies the kind of operation within an execute block.
type ExecuteOperationType string

const (
	ExecuteOpInvokeAction   ExecuteOperationType = "invoke_action"
	ExecuteOpReadDataSource ExecuteOperationType = "read_datasource"
)

// ExecuteOperation represents a single ordered operation within an execute block.
type ExecuteOperation struct {
	Type      ExecuteOperationType
	Traversal hcl.Traversal
}

// Execution represents an execute block within a step.
type Execution struct {
	InvokeAction    []hcl.Traversal
	ReadDataSources []hcl.Traversal
	// Operations preserves declaration order so execution can interleave
	// invoke_action and read_datasource in the user's intended sequence.
	Operations []ExecuteOperation
}

func decodeExecutionBlock(block *hcl.Block) (*Execution, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	exec := &Execution{
		InvokeAction:    make([]hcl.Traversal, 0),
		ReadDataSources: make([]hcl.Traversal, 0),
		Operations:      make([]ExecuteOperation, 0),
	}

	content, moreDiags := block.Body.Content(executeSchema)
	diags = append(diags, moreDiags...)
	if len(content.Blocks) == 0 {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Empty execute block",
			Detail:   "An execute block must contain at least one invoke_action or read_datasource block.",
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
			exec.Operations = append(exec.Operations, ExecuteOperation{
				Type:      ExecuteOpInvokeAction,
				Traversal: traversal,
			})

		case "read_datasource":
			readContent, readDiags := innerBlock.Body.Content(readDataSourceSchema)
			diags = append(diags, readDiags...)

			attr := readContent.Attributes["datasource"]
			if attr == nil {
				continue
			}

			traversal, traversalDiags := hcl.AbsTraversalForExpr(attr.Expr)
			diags = append(diags, traversalDiags...)
			if traversalDiags.HasErrors() {
				continue
			}

			exec.ReadDataSources = append(exec.ReadDataSources, traversal)
			exec.Operations = append(exec.Operations, ExecuteOperation{
				Type:      ExecuteOpReadDataSource,
				Traversal: traversal,
			})
		}
	}

	return exec, diags
}

var executeSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{
			Type: "invoke_action",
		},
		{
			Type: "read_datasource",
		},
	},
}

var readDataSourceSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{
			Name:     "datasource",
			Required: true,
		},
	},
}
