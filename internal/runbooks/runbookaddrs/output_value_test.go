// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
)

func TestParseAbsOutputValueStr(t *testing.T) {
	tests := []struct {
		input string
		want  StepOutputValue
	}{
		{
			input: "step.deploy.result",
			want: StepOutputValue{
				Step: StepInstance{Step: Step{Name: "deploy"}},
				Name: "result",
			},
		},
		{
			input: "step.deploy[\"blue\"].result",
			want: StepOutputValue{
				Step: StepInstance{
					Step: Step{Name: "deploy"},
					Key:  addrs.StringKey("blue"),
				},
				Name: "result",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, diags := ParseAbsOutputValueStr(test.input)
			if diags.HasErrors() {
				t.Fatalf("unexpected diagnostics: %s", diags.Err())
			}
			if got != test.want {
				t.Fatalf("wrong result\ngot:  %#v\nwant: %#v", got, test.want)
			}
		})
	}
}
