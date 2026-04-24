package runbookgraph

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

const reservedRunbookStepSymbol = "step"

func validateReservedRunbookSymbolsInExpr(expr hcl.Expression) tfdiags.Diagnostics {
	if expr == nil {
		return nil
	}

	node, ok := expr.(hclsyntax.Node)
	if !ok {
		return nil
	}

	var diags hcl.Diagnostics
	visitDiags := hclsyntax.VisitAll(node, func(node hclsyntax.Node) hcl.Diagnostics {
		forExpr, ok := node.(*hclsyntax.ForExpr)
		if !ok {
			return nil
		}
		if forExpr.KeyVar != reservedRunbookStepSymbol && forExpr.ValVar != reservedRunbookStepSymbol {
			return nil
		}
		return hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Reserved runbook symbol",
			Detail:   "The identifier \"step\" is reserved for runbook step references and cannot be used as a temporary variable in for expressions. Use a different variable name.",
			Subject:  forExpr.SrcRange.Ptr(),
		}}
	})
	diags = append(diags, visitDiags...)
	return tfdiags.Diagnostics{}.Append(diags)
}

func validateReservedRunbookSymbolsInBody(body hcl.Body) tfdiags.Diagnostics {
	if body == nil {
		return nil
	}

	var diags tfdiags.Diagnostics
	attrs, _ := body.JustAttributes()
	for _, attr := range attrs {
		diags = diags.Append(validateReservedRunbookSymbolsInExpr(attr.Expr))
	}
	content, _, _ := body.PartialContent(&hcl.BodySchema{})
	for _, block := range content.Blocks {
		diags = diags.Append(validateReservedRunbookSymbolsInBody(block.Body))
	}
	return diags
}
