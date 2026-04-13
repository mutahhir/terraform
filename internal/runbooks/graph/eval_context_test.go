// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"testing"

	"github.com/hashicorp/terraform/internal/configs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/zclconf/go-cty/cty"
)

func TestEvalContextVariableAccess(t *testing.T) {
	ctx := NewEvalContext(EvalContextOpts{})

	ctx.SetVariable("name", &terraform.InputValue{
		Value: cty.StringVal("hello"),
	})

	value, ok := ctx.GetVariable("name")
	if !ok {
		t.Fatal("expected variable to be available")
	}
	if value == nil {
		t.Fatal("expected variable value to be non-nil")
	}
	if got := value.Value.AsString(); got != "hello" {
		t.Fatalf("wrong value %q", got)
	}
}

func TestEvalContextWorkspaceConfig(t *testing.T) {
	config := &runbookconfigs.RunbookConfig{
		WorkspaceConfig: &configs.Config{},
	}

	ctx := NewEvalContext(EvalContextOpts{Config: config})
	if ctx.Config() != config {
		t.Fatal("expected config accessor to return original config")
	}
	if ctx.WorkspaceConfig() != config.WorkspaceConfig {
		t.Fatal("expected workspace config accessor to return original workspace config")
	}

	ctx = NewEvalContext(EvalContextOpts{Config: &runbookconfigs.RunbookConfig{}})
	if ctx.WorkspaceConfig() != nil {
		t.Fatal("expected nil workspace config")
	}

	ctx = NewEvalContext(EvalContextOpts{})
	if ctx.WorkspaceConfig() != nil {
		t.Fatal("expected nil workspace config when no runbook config is set")
	}
}
