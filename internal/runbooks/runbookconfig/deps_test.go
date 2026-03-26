// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

func TestAnalyzeDependenciesInfersIndexedStepReferences(t *testing.T) {
	rootDir := t.TempDir()

	file, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "invoke" {
  for_each = {
    primary = "one"
  }

  execute {}

  output "invocation_output" {
    value = each.value
  }
}

step "summary" {
  output "primary" {
    value = steps.invoke["primary"].invocation_output
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	deps, diags := AnalyzeDependencies(&Config{
		RootPath: rootDir,
		Files: map[string]*File{
			file.Path: file,
		},
		Runbook: file.Runbook,
	})
	tfdiags.AssertNoDiagnostics(t, diags)

	if diff := cmp.Diff([]string{"invoke"}, deps.Dependencies["summary"]); diff != "" {
		t.Fatalf("wrong inferred dependencies (-want +got):\n%s", diff)
	}
}

func TestAnalyzeDependenciesInfersWholeStepReferences(t *testing.T) {
	rootDir := t.TempDir()

	file, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "invoke" {
  for_each = {
    primary = "one"
  }

  execute {}

  output "invocation_output" {
    value = each.value
  }
}

step "summary" {
  output "primary" {
    value = one([for step in values(steps.invoke) : step.invocation_output])
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	deps, diags := AnalyzeDependencies(&Config{
		RootPath: rootDir,
		Files: map[string]*File{
			file.Path: file,
		},
		Runbook: file.Runbook,
	})
	tfdiags.AssertNoDiagnostics(t, diags)

	if diff := cmp.Diff([]string{"invoke"}, deps.Dependencies["summary"]); diff != "" {
		t.Fatalf("wrong inferred dependencies (-want +got):\n%s", diff)
	}
}
