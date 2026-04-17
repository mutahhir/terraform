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
		return parseWorkspaceRef(traversal)
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

func parseWorkspaceRef(traversal hcl.Traversal) (*Reference, tfdiags.Diagnostics) {
	if len(traversal) < 2 {
		return nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid reference",
			Detail:   "Expected additional traversal after the workspace symbol.",
			Subject:  traversal.SourceRange().Ptr(),
		})
	}

	var modulePath WorkspaceModulePath
	idx := 1
	for idx < len(traversal) {
		attr, ok := traversal[idx].(hcl.TraverseAttr)
		if !ok {
			break
		}
		if attr.Name != "module" {
			break
		}
		if idx+1 >= len(traversal) {
			return nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid workspace reference",
				Detail:   "Expected a module call name after \".module\".",
				Subject:  traversal[idx].SourceRange().Ptr(),
			})
		}
		nameAttr, ok := traversal[idx+1].(hcl.TraverseAttr)
		if !ok {
			return nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid workspace reference",
				Detail:   "Expected a module call name after \".module\".",
				Subject:  traversal[idx+1].SourceRange().Ptr(),
			})
		}
		call := WorkspaceModuleCall{Name: nameAttr.Name, InstanceKey: terraformaddrs.NoKey}
		idx += 2
		if idx < len(traversal) {
			if index, ok := traversal[idx].(hcl.TraverseIndex); ok {
				parsed, err := terraformaddrs.ParseInstanceKey(index.Key)
				if err == nil {
					call.InstanceKey = parsed
					idx++
				}
			}
		}
		modulePath.Calls = append(modulePath.Calls, call)
	}

	if idx >= len(traversal) {
		return nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid workspace reference",
			Detail:   "Expected a workspace resource, data source, or action after the workspace path.",
			Subject:  traversal.SourceRange().Ptr(),
		})
	}

	attr, ok := traversal[idx].(hcl.TraverseAttr)
	if !ok {
		return nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid workspace reference",
			Detail:   "Expected an attribute name after the workspace path.",
			Subject:  traversal[idx].SourceRange().Ptr(),
		})
	}

	switch attr.Name {
	case "action":
		typ, name, rng, remain, diags := parseDoubleAttrRef(traversal[idx:])
		return &Reference{Subject: WorkspaceAction{Module: modulePath, Action: terraformaddrs.Action{Type: typ, Name: name}}, SourceRange: tfdiags.SourceRangeFromHCL(hcl.RangeBetween(traversal[0].SourceRange(), rng)), Remaining: remain}, diags
	case "data":
		typ, name, rng, remain, diags := parseDoubleAttrRef(traversal[idx:])
		return &Reference{Subject: WorkspaceResource{Module: modulePath, Resource: terraformaddrs.Resource{Mode: terraformaddrs.DataResourceMode, Type: typ, Name: name}}, SourceRange: tfdiags.SourceRangeFromHCL(hcl.RangeBetween(traversal[0].SourceRange(), rng)), Remaining: remain}, diags
	default:
		if idx+1 >= len(traversal) {
			return nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid workspace reference",
				Detail:   "Expected a resource name after the workspace resource type.",
				Subject:  traversal[idx].SourceRange().Ptr(),
			})
		}
		nameAttr, ok := traversal[idx+1].(hcl.TraverseAttr)
		if !ok {
			return nil, tfdiags.Diagnostics{}.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid workspace reference",
				Detail:   "Expected a resource name after the workspace resource type.",
				Subject:  traversal[idx+1].SourceRange().Ptr(),
			})
		}
		resource := terraformaddrs.Resource{Mode: terraformaddrs.ManagedResourceMode, Type: attr.Name, Name: nameAttr.Name}
		remain := traversal[idx+2:]
		if len(remain) > 0 {
			if index, ok := remain[0].(hcl.TraverseIndex); ok {
				parsed, err := terraformaddrs.ParseInstanceKey(index.Key)
				if err == nil {
					resource.Name = resource.Name
					remain = remain[1:]
					_ = parsed
				}
			}
		}
		rng := hcl.RangeBetween(traversal[0].SourceRange(), traversal[idx+1].SourceRange())
		return &Reference{Subject: WorkspaceResource{Module: modulePath, Resource: resource}, SourceRange: tfdiags.SourceRangeFromHCL(rng), Remaining: remain}, nil
	}
}
