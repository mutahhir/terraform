// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package addrs

import (
	"testing"

	"github.com/go-test/deep"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/hashicorp/terraform/internal/tfdiags"
)

func TestParseRefInRunbookScope(t *testing.T) {
	tests := []struct {
		Input   string
		Want    *Reference
		WantErr string
	}{
		{
			`steps.deploy["blue"].status`,
			&Reference{
				Subject: Step{Name: "deploy"}.Instance(StringKey("blue")),
				SourceRange: tfdiags.SourceRange{
					Start: tfdiags.SourcePos{Line: 1, Column: 1, Byte: 0},
					End:   tfdiags.SourcePos{Line: 1, Column: 21, Byte: 20},
				},
				Remaining: hcl.Traversal{hcl.TraverseAttr{Name: "status", SrcRange: hcl.Range{Start: hcl.Pos{Line: 1, Column: 21, Byte: 20}, End: hcl.Pos{Line: 1, Column: 28, Byte: 27}}}},
			},
			``,
		},
		{
			`workspace.output.cluster.name`,
			&Reference{
				Subject: WorkspaceOutput{Name: "cluster"},
				SourceRange: tfdiags.SourceRange{
					Start: tfdiags.SourcePos{Line: 1, Column: 1, Byte: 0},
					End:   tfdiags.SourcePos{Line: 1, Column: 25, Byte: 24},
				},
				Remaining: hcl.Traversal{hcl.TraverseAttr{Name: "name", SrcRange: hcl.Range{Start: hcl.Pos{Line: 1, Column: 25, Byte: 24}, End: hcl.Pos{Line: 1, Column: 30, Byte: 29}}}},
			},
			``,
		},
		{
			`list.test_resource.inventory.data`,
			&Reference{
				Subject: ResourceInstance{Resource: Resource{Mode: ListResourceMode, Type: "test_resource", Name: "inventory"}},
				SourceRange: tfdiags.SourceRange{
					Start: tfdiags.SourcePos{Line: 1, Column: 1, Byte: 0},
					End:   tfdiags.SourcePos{Line: 1, Column: 29, Byte: 28},
				},
				Remaining: hcl.Traversal{hcl.TraverseAttr{Name: "data", SrcRange: hcl.Range{Start: hcl.Pos{Line: 1, Column: 29, Byte: 28}, End: hcl.Pos{Line: 1, Column: 34, Byte: 33}}}},
			},
			``,
		},
		{
			`action.test_action.target.result`,
			&Reference{
				Subject: ActionInstance{Action: Action{Type: "test_action", Name: "target"}, Key: NoKey},
				SourceRange: tfdiags.SourceRange{
					Start: tfdiags.SourcePos{Line: 1, Column: 1, Byte: 0},
					End:   tfdiags.SourcePos{Line: 1, Column: 26, Byte: 25},
				},
				Remaining: hcl.Traversal{hcl.TraverseAttr{Name: "result", SrcRange: hcl.Range{Start: hcl.Pos{Line: 1, Column: 26, Byte: 25}, End: hcl.Pos{Line: 1, Column: 33, Byte: 32}}}},
			},
			``,
		},
		{
			`steps`,
			nil,
			`The "steps" object cannot be accessed directly. Instead, access one of its attributes.`,
		},
		{
			`workspace`,
			nil,
			`The "workspace" object cannot be accessed directly. Instead, access one of its attributes.`,
		},
		{
			`list`,
			nil,
			`The "list" object must be followed by two attribute names: the list resource type and the resource name.`,
		},
		{
			`action`,
			nil,
			`The "action" object must be followed by two attribute names: the action type and the action name.`,
		},
	}

	for _, test := range tests {
		t.Run(test.Input, func(t *testing.T) {
			traversal, travDiags := hclsyntax.ParseTraversalAbs([]byte(test.Input), "", hcl.Pos{Line: 1, Column: 1})
			if travDiags.HasErrors() {
				t.Fatal(travDiags.Error())
			}

			got, diags := ParseRefFromRunbookScope(traversal)

			switch len(diags) {
			case 0:
				if test.WantErr != "" {
					t.Fatalf("succeeded; want error: %s", test.WantErr)
				}
			case 1:
				if test.WantErr == "" {
					t.Fatalf("unexpected diagnostics: %s", diags.Err())
				}
				if got, want := diags[0].Description().Detail, test.WantErr; got != want {
					t.Fatalf("wrong error\ngot:  %s\nwant: %s", got, want)
				}
			default:
				t.Fatalf("too many diagnostics: %s", diags.Err())
			}

			if diags.HasErrors() {
				return
			}

			for _, problem := range deep.Equal(got, test.Want) {
				t.Error(problem)
			}
		})
	}
}

func TestParseRefStrFromRunbookScope(t *testing.T) {
	got, diags := ParseRefStrFromRunbookScope(`steps.deploy["blue"].status`)
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	if got == nil {
		t.Fatal("expected reference")
	}
	if got.Subject.String() != `steps.deploy["blue"]` {
		t.Fatalf("wrong string form: %s", got.Subject.String())
	}
	if len(got.Remaining) != 1 {
		t.Fatalf("wrong remaining traversal: %#v", got.Remaining)
	}
	if idx, ok := got.Subject.(StepInstance); !ok || idx.Key.Value() != cty.StringVal("blue") {
		t.Fatalf("wrong step instance subject: %#v", got.Subject)
	}
}
