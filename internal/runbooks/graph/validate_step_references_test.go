// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"strings"
	"testing"

	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/spf13/afero"
)

func TestValidateStepReferences_ValidInterStepReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "first" {
  output "result" {
    value = "hello"
  }
}

step "second" {
  output "combined" {
    value = step.first.result
  }
}
`)

	config := loadIntegrationRunbookConfig(t, fs)
	diags := validateStepReferences(config)
	if diags.HasErrors() {
		t.Fatalf("expected no errors, got: %s", diags.Err())
	}
}

func TestValidateStepReferences_UndeclaredStep(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "first" {
  output "result" {
    value = step.nonexistent.some_output
  }
}
`)

	config := loadIntegrationRunbookConfig(t, fs)
	diags := validateStepReferences(config)
	if !diags.HasErrors() {
		t.Fatal("expected errors for reference to undeclared step, got none")
	}
	errStr := diags.Err().Error()
	if !strings.Contains(errStr, "undeclared step") {
		t.Fatalf("expected 'undeclared step' in error, got: %s", errStr)
	}
	if !strings.Contains(errStr, "nonexistent") {
		t.Fatalf("expected 'nonexistent' in error, got: %s", errStr)
	}
}

func TestValidateStepReferences_UndeclaredOutput(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "first" {
  output "actual_output" {
    value = "hello"
  }
}

step "second" {
  output "combined" {
    value = step.first.wrong_output
  }
}
`)

	config := loadIntegrationRunbookConfig(t, fs)
	diags := validateStepReferences(config)
	if !diags.HasErrors() {
		t.Fatal("expected errors for reference to undeclared output, got none")
	}
	errStr := diags.Err().Error()
	if !strings.Contains(errStr, "undeclared step output") {
		t.Fatalf("expected 'undeclared step output' in error, got: %s", errStr)
	}
	if !strings.Contains(errStr, "wrong_output") {
		t.Fatalf("expected 'wrong_output' in error, got: %s", errStr)
	}
}

func TestValidateStepReferences_SelfReferenceAllowed(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "only" {
  locals {
    x = "hello"
  }
  output "result" {
    value = local.x
  }
}
`)

	config := loadIntegrationRunbookConfig(t, fs)
	diags := validateStepReferences(config)
	if diags.HasErrors() {
		t.Fatalf("expected no errors for intra-step references, got: %s", diags.Err())
	}
}

func TestValidateStepReferences_MultipleErrors(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "first" {
  output "result" {
    value = step.ghost_a.foo
  }
}

step "second" {
  output "result" {
    value = step.ghost_b.bar
  }
}
`)

	config := loadIntegrationRunbookConfig(t, fs)
	diags := validateStepReferences(config)
	if !diags.HasErrors() {
		t.Fatal("expected errors, got none")
	}
	errStr := diags.Err().Error()
	if !strings.Contains(errStr, "ghost_a") {
		t.Fatalf("expected 'ghost_a' in error, got: %s", errStr)
	}
	if !strings.Contains(errStr, "ghost_b") {
		t.Fatalf("expected 'ghost_b' in error, got: %s", errStr)
	}
}

func TestValidateStepReferences_NilConfig(t *testing.T) {
	diags := validateStepReferences(nil)
	if diags.HasErrors() {
		t.Fatalf("expected no errors for nil config, got: %s", diags.Err())
	}
}

func TestValidateStepReferences_EmptyConfig(t *testing.T) {
	config := &runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{},
	}
	diags := validateStepReferences(config)
	if diags.HasErrors() {
		t.Fatalf("expected no errors for empty config, got: %s", diags.Err())
	}
}
