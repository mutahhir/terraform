// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookgraph"
)

type WalkerData struct {
	Context *RunbookContext
}

type RunbookGraphWalker = runbookgraph.Walker[*PlanGraph, *WalkerData]

func newRunbookGraphWalker(ctx *RunbookContext, graph *PlanGraph, operation runbookgraph.WalkOperation) *RunbookGraphWalker {
	return runbookgraph.NewWalker(&WalkerData{Context: ctx}, graph, operation)
}

func (c *RunbookContext) resetValidationTracking() {
	if c == nil {
		return
	}
	c.usedWorkspaceOutputNames = c.usedWorkspaceOutputNames[:0]
	for stepName := range c.workspaceOutputsByStep {
		c.workspaceOutputsByStep[stepName] = c.workspaceOutputsByStep[stepName][:0]
	}
}

func visitBodyExpressions(body hcl.Body, visit func(hcl.Expression)) {
	if body == nil || visit == nil {
		return
	}

	syntaxBody, ok := body.(*hclsyntax.Body)
	if !ok {
		return
	}

	for _, attr := range syntaxBody.Attributes {
		visit(attr.Expr)
	}
	for _, block := range syntaxBody.Blocks {
		visitBodyExpressions(block.Body, visit)
	}
}

func runbookReferencesInExpr(scope runbookgraph.Scope, exprs ...hcl.Expression) []runbookgraph.Reference {
	var refs []runbookgraph.Reference
	for _, expr := range exprs {
		if expr == nil {
			continue
		}
		for _, traversal := range expr.Variables() {
			if ref, ok := runbookReferenceFromTraversal(scope, traversal); ok {
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

func runbookReferenceFromTraversal(scope runbookgraph.Scope, traversal hcl.Traversal) (runbookgraph.Reference, bool) {
	if traversal == nil {
		return runbookgraph.Reference{}, false
	}
	if _, ok := scope.(runbookgraph.RootScope); ok {
		switch traversal.RootName() {
		case "var":
			ref, diags := runbookaddrs.ParseRunbookReference(traversal)
			if diags.HasErrors() {
				return runbookgraph.Reference{}, false
			}
			if target, ok := ref.Target.(addrs.InputVariable); ok {
				return runbookgraph.Reference{Target: target, Scope: runbookgraph.RootScope{}, SourceRange: ref.SourceRange}, true
			}
		case "step":
			ref, _, diags := runbookaddrs.ParseStepOutputReference(traversal)
			if diags.HasErrors() {
				return runbookgraph.Reference{}, false
			}
			if target, ok := ref.Target.(runbookaddrs.StepOutputValue); ok {
				return runbookgraph.Reference{Target: target.ConfigStepOutputValue(), Scope: runbookgraph.RootScope{}, SourceRange: ref.SourceRange}, true
			}
		}
		return runbookgraph.Reference{}, false
	}
	if _, ok := scope.(runbookgraph.StepConfigScope); ok {
		return runbookStepScopedReferenceFromTraversal(scope, traversal)
	}
	if _, ok := scope.(runbookgraph.StepInstanceScope); ok {
		return runbookStepScopedReferenceFromTraversal(scope, traversal)
	}
	return runbookgraph.Reference{}, false
}

func runbookStepScopedReferenceFromTraversal(scope runbookgraph.Scope, traversal hcl.Traversal) (runbookgraph.Reference, bool) {
	if traversal == nil {
		return runbookgraph.Reference{}, false
	}
	switch traversal.RootName() {
	case "var":
		ref, diags := runbookaddrs.ParseRunbookReference(traversal)
		if diags.HasErrors() {
			return runbookgraph.Reference{}, false
		}
		if target, ok := ref.Target.(addrs.InputVariable); ok {
			return runbookgraph.Reference{Target: target, Scope: runbookgraph.RootScope{}, SourceRange: ref.SourceRange}, true
		}
	case "step":
		ref, _, diags := runbookaddrs.ParseStepOutputReference(traversal)
		if diags.HasErrors() {
			return runbookgraph.Reference{}, false
		}
		if target, ok := ref.Target.(runbookaddrs.StepOutputValue); ok {
			return runbookgraph.Reference{Target: target.ConfigStepOutputValue(), Scope: runbookgraph.RootScope{}, SourceRange: ref.SourceRange}, true
		}
	case "workspace":
		return runbookgraph.Reference{}, false
	default:
		ref, diags := runbookaddrs.ParseInStepReference(traversal)
		if diags.HasErrors() {
			return runbookgraph.Reference{}, false
		}
		if target := scopedReferenceTarget(ref.Target); target != nil {
			return runbookgraph.Reference{Target: target, Scope: scope, SourceRange: ref.SourceRange}, true
		}
	}
	return runbookgraph.Reference{}, false
}

func scopedReferenceTarget(target any) runbookgraph.ReferenceTarget {
	switch addr := target.(type) {
	case addrs.InputVariable:
		return addr
	case addrs.LocalValue:
		return addr
	case runbookaddrs.ActionInstance:
		return addr
	case runbookaddrs.DataSource:
		return addr
	case runbookaddrs.List:
		return addr
	case runbookaddrs.StepOutputValue:
		return addr
	case runbookaddrs.WorkspaceActionInstance:
		return addr
	case runbookaddrs.WorkspaceOutputValue:
		return addr
	default:
		return nil
	}
}
