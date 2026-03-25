// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

const (
	lowerVarSteps     = "__runbook_steps"
	lowerVarWorkspace = "__runbook_workspace"
	lowerVarActions   = "__runbook_actions"
)

type LowerScope struct {
	scope         EvalScope
	syntheticVars map[string]cty.Value
}

type lowerExprReplacement struct {
	start int
	end   int
	src   string
}

func NewLowerScope(scope EvalScope) *LowerScope {
	return &LowerScope{
		scope:         scope,
		syntheticVars: map[string]cty.Value{},
	}
}

func (s *LowerScope) RewriteExpr(expr hcl.Expression, exprSrc []byte) ([]byte, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if s == nil || expr == nil || len(bytes.TrimSpace(exprSrc)) == 0 {
		return exprSrc, diags
	}

	exprRange := expr.Range()
	replacements := make([]lowerExprReplacement, 0)
	for _, traversal := range expr.Variables() {
		ref, refDiags := addrs.ParseRefFromRunbookScope(traversal)
		diags = diags.Append(refDiags)
		if refDiags.HasErrors() || ref == nil {
			continue
		}

		var (
			varName string
			varVal  cty.Value
		)
		switch ref.Subject.(type) {
		case addrs.Step, addrs.StepInstance:
			varName = lowerVarSteps
			varVal = normalizeScopeValue(s.scope.Steps)
		case addrs.RunbookAction:
			varName = lowerVarActions
			varVal = normalizeScopeValue(s.scope.Actions)
		case addrs.WorkspaceOutput:
			varName = lowerVarWorkspace
			varVal = normalizeScopeValue(s.scope.Workspace)
		default:
			continue
		}

		s.syntheticVars[varName] = varVal

		rng := traversal.SourceRange()
		start := rng.Start.Byte - exprRange.Start.Byte
		end := rng.End.Byte - exprRange.Start.Byte
		if start < 0 || end < start || end > len(exprSrc) {
			continue
		}
		original := string(exprSrc[start:end])
		root := traversal.RootName()
		replacements = append(replacements, lowerExprReplacement{
			start: start,
			end:   end,
			src:   "var." + varName + strings.TrimPrefix(original, root),
		})
	}

	if len(replacements) == 0 {
		return exprSrc, diags
	}

	sort.Slice(replacements, func(i, j int) bool {
		return replacements[i].start > replacements[j].start
	})

	ret := string(exprSrc)
	for _, replacement := range replacements {
		ret = ret[:replacement.start] + replacement.src + ret[replacement.end:]
	}
	return []byte(ret), diags
}

func (s *LowerScope) AppendVariableBlocks(body *hclwrite.Body) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if s == nil || body == nil || len(s.syntheticVars) == 0 {
		return diags
	}

	names := make([]string, 0, len(s.syntheticVars))
	for name := range s.syntheticVars {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		block := body.AppendNewBlock("variable", []string{name})
		block.Body().SetAttributeRaw("default", hclwrite.TokensForValue(sanitizeValueForLowering(s.syntheticVars[name])))
		body.AppendNewline()
	}

	return diags
}

func sanitizeValueForLowering(v cty.Value) cty.Value {
	if v == cty.NilVal {
		return cty.NullVal(cty.DynamicPseudoType)
	}
	if !v.IsKnown() {
		return cty.NullVal(cty.DynamicPseudoType)
	}
	if v.IsNull() {
		return v
	}
	if v.Type().IsObjectType() {
		vals := make(map[string]cty.Value)
		for name, attr := range v.AsValueMap() {
			vals[name] = sanitizeValueForLowering(attr)
		}
		return cty.ObjectVal(vals)
	}
	if v.Type().IsMapType() {
		vals := make(map[string]cty.Value)
		for name, attr := range v.AsValueMap() {
			vals[name] = sanitizeValueForLowering(attr)
		}
		return cty.MapVal(vals)
	}
	if v.Type().IsTupleType() || v.Type().IsListType() || v.Type().IsSetType() {
		vals := make([]cty.Value, 0, v.LengthInt())
		it := v.ElementIterator()
		for it.Next() {
			_, elem := it.Element()
			vals = append(vals, sanitizeValueForLowering(elem))
		}
		switch {
		case v.Type().IsTupleType():
			return cty.TupleVal(vals)
		case v.Type().IsListType():
			if len(vals) == 0 {
				return cty.ListValEmpty(v.Type().ElementType())
			}
			return cty.ListVal(vals)
		default:
			if len(vals) == 0 {
				return cty.SetValEmpty(v.Type().ElementType())
			}
			return cty.SetVal(vals)
		}
	}
	return v
}

func (s *LowerScope) RewriteBodyExpressions(body *hclwrite.Body) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if s == nil || body == nil {
		return diags
	}

	for name, attr := range body.Attributes() {
		if attr == nil {
			continue
		}
		attrSrc := bytes.TrimSpace(attr.Expr().BuildTokens(nil).Bytes())
		expr, parseDiags := hclsyntax.ParseExpression(attrSrc, "rewrite_attr.hcl", hcl.InitialPos)
		diags = diags.Append(parseDiags)
		if parseDiags.HasErrors() || expr == nil {
			continue
		}
		rewritten, moreDiags := s.RewriteExpr(expr, attrSrc)
		diags = diags.Append(moreDiags)
		if moreDiags.HasErrors() {
			continue
		}
		toks, tokenDiags := expressionTokens(rewritten)
		diags = diags.Append(tokenDiags)
		if tokenDiags.HasErrors() {
			continue
		}
		body.SetAttributeRaw(name, toks)
	}

	for _, block := range body.Blocks() {
		diags = diags.Append(s.RewriteBodyExpressions(block.Body()))
	}

	return diags
}

func expressionTokens(src []byte) (hclwrite.Tokens, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	snippet := []byte(fmt.Sprintf("value = %s\n", bytes.TrimSpace(src)))
	parsed, parseDiags := hclwrite.ParseConfig(snippet, "rewrite_expr.hcl", hcl.InitialPos)
	if parseDiags.HasErrors() || parsed == nil {
		return nil, diags.Append(parseDiags)
	}
	attr := parsed.Body().GetAttribute("value")
	if attr == nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Invalid lowered expression", "Failed to rebuild rewritten expression tokens."))
	}
	return attr.Expr().BuildTokens(nil), diags
}
