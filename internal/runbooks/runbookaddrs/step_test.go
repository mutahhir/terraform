// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/addrs"
)

func TestParseAbsStepInstanceStr(t *testing.T) {
	tests := []struct {
		input string
		want  StepInstance
	}{
		{
			input: "step.deploy",
			want: StepInstance{
				Step: ConfigStep{Name: "deploy"},
			},
		},
		{
			input: "step.deploy[\"blue\"]",
			want: StepInstance{
				Step: ConfigStep{Name: "deploy"},
				Key:  addrs.StringKey("blue"),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, diags := ParseAbsStepInstanceStr(test.input)
			if diags.HasErrors() {
				t.Fatalf("unexpected diagnostics: %s", diags.Err())
			}
			if got != test.want {
				t.Fatalf("wrong result\ngot:  %#v\nwant: %#v", got, test.want)
			}
		})
	}
}

func TestParseAbsStepInstanceStrOnly(t *testing.T) {
	got, remain, diags := ParseAbsStepInstanceStrOnly("step.deploy.result")
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if got != (StepInstance{Step: ConfigStep{Name: "deploy"}}) {
		t.Fatalf("wrong step instance\ngot:  %#v", got)
	}
	if len(remain) != 1 {
		t.Fatalf("wrong remain length\ngot:  %d\nwant: %d", len(remain), 1)
	}
	attr, ok := remain[0].(hcl.TraverseAttr)
	if !ok {
		t.Fatalf("wrong remain step type\ngot:  %T", remain[0])
	}
	if got, want := attr.Name, "result"; got != want {
		t.Fatalf("wrong remain attr\ngot:  %q\nwant: %q", got, want)
	}
}

func TestInStepString(t *testing.T) {
	addr := ConfigAction{
		Step: ConfigStep{Name: "deploy"},
		Item: addrs.ConfigAction{
			Action: addrs.Action{Type: "http", Name: "notify"},
		},
	}

	if got, want := addr.String(), "step.deploy.action.http.notify"; got != want {
		t.Fatalf("wrong string\ngot:  %q\nwant: %q", got, want)
	}
}

func TestInAbsStepInstanceString(t *testing.T) {
	addr := AbsActionInvocationInstance{
		Step: StepInstance{Step: ConfigStep{Name: "deploy"}},
		Item: addrs.AbsActionInstance{
			Action: addrs.ActionInstance{
				Action: addrs.Action{Type: "http", Name: "notify"},
			},
		},
	}

	if got, want := addr.String(), "step.deploy.action.http.notify"; got != want {
		t.Fatalf("wrong string\ngot:  %q\nwant: %q", got, want)
	}
}
