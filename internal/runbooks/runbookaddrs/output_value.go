// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/hashicorp/terraform/internal/collections"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// ConfigStepOutputValue is the address of an output value within a step
// configuration.
type ConfigStepOutputValue struct {
	Step Step
	Name string
}

func (v ConfigStepOutputValue) String() string {
	return v.Step.String() + "." + v.Name
}

func (v ConfigStepOutputValue) UniqueKey() collections.UniqueKey[ConfigStepOutputValue] {
	return configOutputValueKey{
		stepKey:   v.Step.UniqueKey(),
		outputKey: v.Name,
	}
}

type configOutputValueKey struct {
	stepKey   collections.UniqueKey[Step]
	outputKey string
}

// IsUniqueKey implements collections.UniqueKey.
func (configOutputValueKey) IsUniqueKey(ConfigStepOutputValue) {}

// StepOutputValue is the address of an output value referenced from a step,
// optionally selecting a specific step instance.
type StepOutputValue struct {
	Step StepInstance
	Name string
}

func (StepOutputValue) referenceableSigil() {}

func (v StepOutputValue) String() string {
	return v.Step.String() + "." + v.Name
}

func (v StepOutputValue) UniqueKey() collections.UniqueKey[StepOutputValue] {
	return outputValueInstanceKey{
		stepKey:   v.Step.UniqueKey(),
		outputKey: v.Name,
	}
}

type outputValueInstanceKey struct {
	stepKey   collections.UniqueKey[StepInstance]
	outputKey string
}

// IsUniqueKey implements collections.UniqueKey.
func (outputValueInstanceKey) IsUniqueKey(StepOutputValue) {}

func (v StepOutputValue) ConfigStepOutputValue() ConfigStepOutputValue {
	return ConfigStepOutputValue{
		Step: v.Step.Step,
		Name: v.Name,
	}
}

func ParseStepOutputValueStr(s string) (StepOutputValue, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	traversal, hclDiags := hclsyntax.ParseTraversalAbs([]byte(s), "", hcl.InitialPos)
	diags = diags.Append(hclDiags)
	if diags.HasErrors() {
		return StepOutputValue{}, diags
	}

	ret, moreDiags := ParseStepOutputValue(traversal)
	return ret, diags.Append(moreDiags)
}

func ParseStepOutputValue(traversal hcl.Traversal) (StepOutputValue, tfdiags.Diagnostics) {
	stepInst, remain, diags := ParseStepInstanceOnly(traversal)
	if diags.HasErrors() {
		return StepOutputValue{}, diags
	}

	if len(remain) != 1 {
		return StepOutputValue{}, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid output address",
			Detail:   "The output address must include the keyword \"step\" followed by a step name and then an output name.",
			Subject:  traversal.SourceRange().Ptr(),
		})
	}

	nameStep, ok := remain[0].(hcl.TraverseAttr)
	if !ok {
		return StepOutputValue{}, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid output address",
			Detail:   "The output address must include the keyword \"step\" followed by a step name and then an output name.",
			Subject:  remain[0].SourceRange().Ptr(),
		})
	}

	return StepOutputValue{
		Step: stepInst,
		Name: nameStep.Name,
	}, diags
}

// ParseOutputValueInstanceStr is retained as a compatibility wrapper.
func ParseOutputValueInstanceStr(s string) (StepOutputValue, tfdiags.Diagnostics) {
	return ParseStepOutputValueStr(s)
}

// ParseOutputValueInstance is retained as a compatibility wrapper.
func ParseOutputValueInstance(traversal hcl.Traversal) (StepOutputValue, tfdiags.Diagnostics) {
	return ParseStepOutputValue(traversal)
}

// ParseAbsOutputValueStr is retained as a compatibility wrapper.
func ParseAbsOutputValueStr(s string) (StepOutputValue, tfdiags.Diagnostics) {
	return ParseStepOutputValueStr(s)
}

// ParseAbsOutputValue is retained as a compatibility wrapper.
func ParseAbsOutputValue(traversal hcl.Traversal) (StepOutputValue, tfdiags.Diagnostics) {
	return ParseStepOutputValue(traversal)
}
