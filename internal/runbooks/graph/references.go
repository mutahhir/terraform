package runbookgraph

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/configs"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
)

func referencesForStepLocal(local *configs.Local) []runbookaddrs.Referenceable {
	if local == nil || local.Expr == nil {
		return nil
	}
	return referencesInExpr(local.Expr)
}

func referencesForStepAction(action *configs.Action) []runbookaddrs.Referenceable {
	if action == nil {
		return nil
	}

	var refs []runbookaddrs.Referenceable
	refs = append(refs, referencesInExpr(action.Count)...)
	refs = append(refs, referencesInExpr(action.ForEach)...)
	if action.Config != nil {
		refs = append(refs, referencesInBody(action.Config)...)
	}
	return refs
}

func referencesForStepResource(resource *configs.Resource) []runbookaddrs.Referenceable {
	if resource == nil {
		return nil
	}

	var refs []runbookaddrs.Referenceable
	refs = append(refs, referencesInExpr(resource.Count)...)
	refs = append(refs, referencesInExpr(resource.ForEach)...)
	if resource.Config != nil {
		refs = append(refs, referencesInBody(resource.Config)...)
	}
	if resource.List != nil {
		refs = append(refs, referencesInExpr(resource.List.IncludeResource)...)
		refs = append(refs, referencesInExpr(resource.List.Limit)...)
	}
	return refs
}

func referencesForStepCondition(condition *runbookconfigs.Condition) []runbookaddrs.Referenceable {
	if condition == nil {
		return nil
	}

	var refs []runbookaddrs.Referenceable
	refs = append(refs, referencesInExpr(condition.Condition)...)
	refs = append(refs, referencesInExpr(condition.ErrorMessage)...)
	return refs
}

func referencesForStepOutput(output *configs.Output) []runbookaddrs.Referenceable {
	if output == nil {
		return nil
	}

	var refs []runbookaddrs.Referenceable
	refs = append(refs, referencesInExpr(output.Expr)...)
	for _, traversal := range output.DependsOn {
		if ref, ok := stepReferenceFromTraversal(traversal); ok {
			refs = append(refs, ref.Subject)
		}
	}
	for _, condition := range output.Preconditions {
		refs = append(refs, referencesInExpr(condition.Condition)...)
		refs = append(refs, referencesInExpr(condition.ErrorMessage)...)
	}
	return refs
}

func referencesForStepExecution(execution *runbookconfigs.Execution) []runbookaddrs.Referenceable {
	if execution == nil {
		return nil
	}

	refs := make([]runbookaddrs.Referenceable, 0, len(execution.InvokeAction)+len(execution.ReadDataSources))
	for _, traversal := range execution.InvokeAction {
		if ref, ok := stepReferenceFromTraversal(traversal); ok {
			refs = append(refs, ref.Subject)
		}
	}
	for _, traversal := range execution.ReadDataSources {
		if ref, ok := stepReferenceFromTraversal(traversal); ok {
			refs = append(refs, ref.Subject)
		}
	}
	return refs
}

func referencesInExpr(expr hcl.Expression) []runbookaddrs.Referenceable {
	if expr == nil {
		return nil
	}

	traversals := expr.Variables()
	refs := make([]runbookaddrs.Referenceable, 0, len(traversals))
	for _, traversal := range traversals {
		if converted, ok := stepReferenceFromTraversal(traversal); ok {
			refs = append(refs, converted.Subject)
		}
	}
	return refs
}

func referencesInBody(body hcl.Body) []runbookaddrs.Referenceable {
	if body == nil {
		return nil
	}

	return referencesInBodyContent(body)
}

func referencesInBodyContent(body hcl.Body) []runbookaddrs.Referenceable {
	attrs, _ := body.JustAttributes()
	refs := make([]runbookaddrs.Referenceable, 0, len(attrs))
	for _, attr := range attrs {
		for _, traversal := range attr.Expr.Variables() {
			if ref, ok := stepReferenceFromTraversal(traversal); ok {
				refs = append(refs, ref.Subject)
			}
		}
	}

	content, _, _ := body.PartialContent(&hcl.BodySchema{})
	for _, block := range content.Blocks {
		refs = append(refs, referencesInBodyContent(block.Body)...)
	}

	return refs
}

func stepReferenceFromTraversal(traversal hcl.Traversal) (*runbookaddrs.Reference, bool) {
	ref, diags := runbookaddrs.ParseRef(traversal)
	if diags.HasErrors() || ref == nil {
		return nil, false
	}
	return ref, true
}
