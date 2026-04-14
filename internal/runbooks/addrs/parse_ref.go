package addrs

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

func ParseRef(traversal hcl.Traversal) (*Reference, tfdiags.Diagnostics) {
	if len(traversal) == 0 {
		return nil, nil
	}

	root, ok := traversal[0].(hcl.TraverseRoot)
	if !ok {
		return nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid reference",
			Detail:   "Runbook references must begin with a root symbol.",
			Subject:  traversal.SourceRange().Ptr(),
		})
	}

	switch root.Name {
	case "local":
		name, rng, remain, diags := parseSingleAttrRef(traversal)
		return &Reference{Subject: terraformaddrs.LocalValue{Name: name}, SourceRange: tfdiags.SourceRangeFromHCL(rng), Remaining: remain}, diags
	case "action":
		typ, name, rng, remain, diags := parseDoubleAttrRef(traversal)
		return &Reference{Subject: terraformaddrs.Action{Type: typ, Name: name}, SourceRange: tfdiags.SourceRangeFromHCL(rng), Remaining: remain}, diags
	case "workspace":
		if len(traversal) < 4 {
			return nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid reference",
				Detail:   "Expected additional attribute names after the workspace symbol.",
				Subject:  traversal.SourceRange().Ptr(),
			})
		}
		first, ok := traversal[1].(hcl.TraverseAttr)
		if !ok {
			return nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid reference",
				Detail:   "Expected an attribute name after the workspace symbol.",
				Subject:  traversal[1].SourceRange().Ptr(),
			})
		}
		switch first.Name {
		case "action":
			typ, name, rng, remain, diags := parseDoubleAttrRef(traversal[1:])
			return &Reference{Subject: terraformaddrs.Action{Type: typ, Name: name}, SourceRange: tfdiags.SourceRangeFromHCL(rng), Remaining: remain}, diags
		default:
			return nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid runbook reference",
				Detail:   fmt.Sprintf("The symbol %q is not valid in the workspace scope.", first.Name),
				Subject:  traversal[1].SourceRange().Ptr(),
			})
		}
	case "data":
		typ, name, rng, remain, diags := parseDoubleAttrRef(traversal)
		return &Reference{Subject: terraformaddrs.Resource{Mode: terraformaddrs.DataResourceMode, Type: typ, Name: name}, SourceRange: tfdiags.SourceRangeFromHCL(rng), Remaining: remain}, diags
	case "list":
		typ, name, rng, remain, diags := parseDoubleAttrRef(traversal)
		return &Reference{Subject: terraformaddrs.Resource{Mode: terraformaddrs.ListResourceMode, Type: typ, Name: name}, SourceRange: tfdiags.SourceRangeFromHCL(rng), Remaining: remain}, diags
	case "step", "steps":
		stepName, rng, remain, diags := parseSingleAttrRef(traversal)
		if diags.HasErrors() {
			return nil, diags
		}
		if len(remain) == 0 {
			return &Reference{Subject: Step{Step: StepInstance{StepName: stepName, InstanceKey: terraformaddrs.NoKey}}, SourceRange: tfdiags.SourceRangeFromHCL(rng), Remaining: remain}, diags
		}
		firstAttr, ok := remain[0].(hcl.TraverseAttr)
		if ok {
			return &Reference{Subject: StepOutput{Step: StepInstance{StepName: stepName, InstanceKey: terraformaddrs.NoKey}, OutputName: firstAttr.Name}, SourceRange: tfdiags.SourceRangeFromHCL(hcl.RangeBetween(traversal[0].SourceRange(), remain[0].SourceRange())), Remaining: remain[1:]}, diags
		}
		return &Reference{Subject: Step{Step: StepInstance{StepName: stepName, InstanceKey: terraformaddrs.NoKey}}, SourceRange: tfdiags.SourceRangeFromHCL(rng), Remaining: remain}, diags
	default:
		return nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid runbook reference",
			Detail:   fmt.Sprintf("The symbol %q is not valid in the runbook step scope.", root.Name),
			Subject:  traversal[0].SourceRange().Ptr(),
		})
	}
}

func parseSingleAttrRef(traversal hcl.Traversal) (string, hcl.Range, hcl.Traversal, tfdiags.Diagnostics) {
	if len(traversal) < 2 {
		return "", traversal.SourceRange(), nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid reference",
			Detail:   "Expected an attribute name after the root symbol.",
			Subject:  traversal.SourceRange().Ptr(),
		})
	}
	attr, ok := traversal[1].(hcl.TraverseAttr)
	if !ok {
		return "", traversal.SourceRange(), nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid reference",
			Detail:   "Expected an attribute name after the root symbol.",
			Subject:  traversal[1].SourceRange().Ptr(),
		})
	}
	return attr.Name, hcl.RangeBetween(traversal[0].SourceRange(), traversal[1].SourceRange()), traversal[2:], nil
}

func parseDoubleAttrRef(traversal hcl.Traversal) (string, string, hcl.Range, hcl.Traversal, tfdiags.Diagnostics) {
	if len(traversal) < 3 {
		return "", "", traversal.SourceRange(), nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid reference",
			Detail:   "Expected two attribute names after the root symbol.",
			Subject:  traversal.SourceRange().Ptr(),
		})
	}
	first, ok := traversal[1].(hcl.TraverseAttr)
	if !ok {
		return "", "", traversal.SourceRange(), nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid reference",
			Detail:   "Expected two attribute names after the root symbol.",
			Subject:  traversal[1].SourceRange().Ptr(),
		})
	}
	second, ok := traversal[2].(hcl.TraverseAttr)
	if !ok {
		return "", "", traversal.SourceRange(), nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid reference",
			Detail:   "Expected two attribute names after the root symbol.",
			Subject:  traversal[2].SourceRange().Ptr(),
		})
	}
	return first.Name, second.Name, hcl.RangeBetween(traversal[0].SourceRange(), traversal[2].SourceRange()), traversal[3:], nil
}
