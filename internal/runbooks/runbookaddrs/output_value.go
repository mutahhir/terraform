// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/hashicorp/terraform/internal/collections"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// OutputValue is the address of an output value within a step.
//
// Unlike Terraform module output addresses, step outputs are exposed directly as
// attributes of a step, and so the string form is just the output name.
type OutputValue struct {
	Name string
}

func (v OutputValue) String() string {
	return v.Name
}

func (v OutputValue) UniqueKey() collections.UniqueKey[OutputValue] {
	return v
}

// An OutputValue is its own [collections.UniqueKey].
func (OutputValue) IsUniqueKey(OutputValue) {}

// ConfigOutputValue is the address of an output value within a step
// configuration.
type ConfigOutputValue struct {
	Step        Step
	OutputValue OutputValue
}

func (ConfigOutputValue) referenceableSigil() {}

func (v ConfigOutputValue) String() string {
	return v.Step.String() + "." + v.OutputValue.String()
}

func (v ConfigOutputValue) UniqueKey() collections.UniqueKey[ConfigOutputValue] {
	return configOutputValueKey{
		stepKey:   v.Step.UniqueKey(),
		outputKey: v.OutputValue.UniqueKey(),
	}
}

type configOutputValueKey struct {
	stepKey   collections.UniqueKey[Step]
	outputKey collections.UniqueKey[OutputValue]
}

// IsUniqueKey implements collections.UniqueKey.
func (configOutputValueKey) IsUniqueKey(ConfigOutputValue) {}

// AbsOutputValue is the absolute address of an output value within a step
// instance.
type AbsOutputValue struct {
	Step        StepInstance
	OutputValue OutputValue
}

func (AbsOutputValue) referenceableSigil() {}

func (v AbsOutputValue) String() string {
	return v.Step.String() + "." + v.OutputValue.String()
}

func (v AbsOutputValue) UniqueKey() collections.UniqueKey[AbsOutputValue] {
	return absOutputValueKey{
		stepKey:   v.Step.UniqueKey(),
		outputKey: v.OutputValue.UniqueKey(),
	}
}

type absOutputValueKey struct {
	stepKey   collections.UniqueKey[StepInstance]
	outputKey collections.UniqueKey[OutputValue]
}

// IsUniqueKey implements collections.UniqueKey.
func (absOutputValueKey) IsUniqueKey(AbsOutputValue) {}

func (v AbsOutputValue) ConfigOutputValue() ConfigOutputValue {
	return ConfigOutputValue{
		Step: v.Step.Step,
		OutputValue: OutputValue{
			Name: v.OutputValue.Name,
		},
	}
}

func ParseAbsOutputValueStr(s string) (AbsOutputValue, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	traversal, hclDiags := hclsyntax.ParseTraversalAbs([]byte(s), "", hcl.InitialPos)
	diags = diags.Append(hclDiags)
	if diags.HasErrors() {
		return AbsOutputValue{}, diags
	}

	ret, moreDiags := ParseAbsOutputValue(traversal)
	return ret, diags.Append(moreDiags)
}

func ParseAbsOutputValue(traversal hcl.Traversal) (AbsOutputValue, tfdiags.Diagnostics) {
	stepInst, remain, diags := ParseStepInstanceOnly(traversal)
	if diags.HasErrors() {
		return AbsOutputValue{}, diags
	}

	if len(remain) != 1 {
		return AbsOutputValue{}, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid output address",
			Detail:   "The output address must include the keyword \"step\" followed by a step name and then an output name.",
			Subject:  traversal.SourceRange().Ptr(),
		})
	}

	nameStep, ok := remain[0].(hcl.TraverseAttr)
	if !ok {
		return AbsOutputValue{}, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid output address",
			Detail:   "The output address must include the keyword \"step\" followed by a step name and then an output name.",
			Subject:  remain[0].SourceRange().Ptr(),
		})
	}

	return AbsOutputValue{
		Step: stepInst,
		OutputValue: OutputValue{
			Name: nameStep.Name,
		},
	}, diags
}
