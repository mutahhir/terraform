// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/collections"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// ConfigStep is the address of a "step" block within a runbook config.
type ConfigStep struct {
	Name string
}

func (s ConfigStep) String() string {
	return "step." + s.Name
}

func (s ConfigStep) UniqueKey() collections.UniqueKey[ConfigStep] {
	return s
}

// A ConfigStep is its own [collections.UniqueKey].
func (ConfigStep) IsUniqueKey(ConfigStep) {}

// StepInstance is the address of a dynamic instance of a step.
type StepInstance struct {
	Step ConfigStep
	Key  addrs.InstanceKey
}

func (s StepInstance) String() string {
	if s.Key == nil {
		return s.Step.String()
	}
	return s.Step.String() + s.Key.String()
}

func (s StepInstance) UniqueKey() collections.UniqueKey[StepInstance] {
	return s
}

// A StepInstance is its own [collections.UniqueKey].
func (StepInstance) IsUniqueKey(StepInstance) {}

func ParseAbsStepInstance(traversal hcl.Traversal) (StepInstance, tfdiags.Diagnostics) {
	inst, remain, diags := ParseAbsStepInstanceOnly(traversal)
	if diags.HasErrors() {
		return StepInstance{}, diags
	}

	if len(remain) > 0 {
		rng := remain.SourceRange()
		if len(remain) == 0 {
			rng = traversal.SourceRange()
		}
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid step instance address",
			Detail:   "The step instance address must include the keyword \"step\" followed by a step name.",
			Subject:  &rng,
		})
		return StepInstance{}, diags
	}

	return inst, diags
}

func ParseAbsStepInstanceStr(s string) (StepInstance, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	traversal, hclDiags := hclsyntax.ParseTraversalAbs([]byte(s), "", hcl.InitialPos)
	diags = diags.Append(hclDiags)
	if diags.HasErrors() {
		return StepInstance{}, diags
	}

	ret, moreDiags := ParseAbsStepInstance(traversal)
	diags = diags.Append(moreDiags)
	return ret, diags
}

func ParsePartialAbsStepInstanceStr(s string) (StepInstance, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	traversal, hclDiags := hclsyntax.ParseTraversalPartial([]byte(s), "", hcl.InitialPos)
	diags = diags.Append(hclDiags)
	if diags.HasErrors() {
		return StepInstance{}, diags
	}

	ret, moreDiags := ParseAbsStepInstance(traversal)
	diags = diags.Append(moreDiags)
	return ret, diags
}

func ParseAbsStepInstanceStrOnly(s string) (StepInstance, hcl.Traversal, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	traversal, hclDiags := hclsyntax.ParseTraversalPartial([]byte(s), "", hcl.InitialPos)
	diags = diags.Append(hclDiags)
	if diags.HasErrors() {
		return StepInstance{}, traversal, diags
	}

	ret, rest, moreDiags := ParseAbsStepInstanceOnly(traversal)
	diags = diags.Append(moreDiags)
	return ret, rest, diags
}

func ParseAbsStepInstanceOnly(traversal hcl.Traversal) (StepInstance, hcl.Traversal, tfdiags.Diagnostics) {
	if traversal.IsRelative() {
		panic("ParseAbsStepInstanceOnly with relative traversal")
	}

	const diagSummary = "Invalid step instance address"
	var diags tfdiags.Diagnostics
	remain := traversal

	if kwStep, ok := remain[0].(hcl.TraverseRoot); !ok || kwStep.Name != "step" {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  diagSummary,
			Detail:   "The step instance address must include the keyword \"step\" followed by a step name.",
			Subject:  kwStep.SourceRange().Ptr(),
		})
		return StepInstance{}, remain, diags
	}
	remain = remain[1:]

	nameStep, ok := remain[0].(hcl.TraverseAttr)
	if !ok {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  diagSummary,
			Detail:   "The step instance address must include the keyword \"step\" followed by a step name.",
			Subject:  nameStep.SourceRange().Ptr(),
		})
		return StepInstance{}, remain, diags
	}
	remain = remain[1:]
	stepAddr := StepInstance{
		Step: ConfigStep{Name: nameStep.Name},
	}

	if len(remain) > 0 {
		switch instStep := remain[0].(type) {
		case hcl.TraverseIndex:
			var err error
			stepAddr.Key, err = addrs.ParseInstanceKey(instStep.Key)
			if err != nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  diagSummary,
					Detail:   fmt.Sprintf("Invalid instance key: %s.", err),
					Subject:  instStep.SourceRange().Ptr(),
				})
				return StepInstance{}, remain, diags
			}
			remain = remain[1:]
		case hcl.TraverseSplat:
			stepAddr.Key = addrs.WildcardKey
			remain = remain[1:]
		}
	}

	return stepAddr, remain, diags
}
