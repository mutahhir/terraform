// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"

	"github.com/hashicorp/terraform/internal/tfdiags"
)

func TestLowerStepUsesRootProviderSourceMapping(t *testing.T) {
	rootDir := t.TempDir()

	cfg, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    simple = {
      source = "hashicorp/test"
    }
  }
}

provider "simple" {
  region = "us-east-1"
}

step "example" {
  list "simple_resource" "inventory" {
    provider = simple

    config {
      value = "hello"
    }
  }

  action "simple_action" "target" {
    config {
      value = "hello"
    }
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	config := &Config{RootPath: rootDir, Files: map[string]*File{cfg.Path: cfg}, Runbook: cfg.Runbook, Variables: cfg.Variables}
	bundle, diags := LowerStep(config, cfg.Steps["example"])
	tfdiags.AssertNoDiagnostics(t, diags)

	mainSrc := string(bundle.Files["main.tf"])
	if !strings.Contains(mainSrc, `source = "hashicorp/test"`) {
		t.Fatalf("lowered main.tf did not use runbook required_providers mapping:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, "provider \"simple\"") || !strings.Contains(mainSrc, `region = "us-east-1"`) {
		t.Fatalf("lowered main.tf did not preserve top-level provider config:\n%s", mainSrc)
	}
}

func TestLowerStepPreservesVariableBlocksForProviderConfig(t *testing.T) {
	rootDir := t.TempDir()

	cfg, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    simple = {
      source = "hashicorp/test"
    }
  }
}

variable "region" {
  default = "us-east-1"
}

provider "simple" {
  region = var.region
}

step "example" {
  list "simple_resource" "inventory" {
    provider = simple

    config {
      value = "hello"
    }
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	config := &Config{RootPath: rootDir, Files: map[string]*File{cfg.Path: cfg}, Runbook: cfg.Runbook, Variables: cfg.Variables}
	bundle, diags := LowerStep(config, cfg.Steps["example"])
	tfdiags.AssertNoDiagnostics(t, diags)

	mainSrc := string(bundle.Files["main.tf"])
	if !strings.Contains(mainSrc, `variable "region"`) {
		t.Fatalf("lowered main.tf did not preserve variable block for provider config:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `region = var.region`) {
		t.Fatalf("lowered main.tf did not preserve provider variable reference:\n%s", mainSrc)
	}
}

func TestLowerStepPreservesNonDefaultVariableBlocks(t *testing.T) {
	rootDir := t.TempDir()

	cfg, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    simple = {
      source = "hashicorp/test"
    }
  }
}

variable "region" {
  type = string
}

provider "simple" {
  region = var.region
}

step "example" {
  list "simple_resource" "inventory" {
    provider = simple

    config {
      value = "hello"
    }
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	config := &Config{RootPath: rootDir, Files: map[string]*File{cfg.Path: cfg}, Runbook: cfg.Runbook, Variables: cfg.Variables}
	bundle, diags := LowerStep(config, cfg.Steps["example"])
	tfdiags.AssertNoDiagnostics(t, diags)

	mainSrc := string(bundle.Files["main.tf"])
	if !strings.Contains(mainSrc, `variable "region"`) {
		t.Fatalf("lowered main.tf did not preserve non-default variable block:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `type = string`) {
		t.Fatalf("lowered main.tf did not preserve non-default variable schema:\n%s", mainSrc)
	}
}

func TestLowerStepPreservesStepDataSources(t *testing.T) {
	rootDir := t.TempDir()

	cfg, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    simple = {
      source = "hashicorp/test"
    }
  }
}

provider "simple" {}

step "example" {
  data "simple_resource" "current" {
    value = "hello"
  }

  action "simple_action" "target" {
    config {
      value = data.simple_resource.current.value
    }
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	config := &Config{RootPath: rootDir, Files: map[string]*File{cfg.Path: cfg}, Runbook: cfg.Runbook}
	bundle, diags := LowerStep(config, cfg.Steps["example"])
	tfdiags.AssertNoDiagnostics(t, diags)

	mainSrc := string(bundle.Files["main.tf"])
	if !strings.Contains(mainSrc, `data "simple_resource" "current"`) {
		t.Fatalf("lowered main.tf did not preserve step data block:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `value = data.simple_resource.current.value`) {
		t.Fatalf("lowered main.tf did not preserve references to step data source:\n%s", mainSrc)
	}
}

func TestLowerStepBuildsSourceMapsForAppendedBlocks(t *testing.T) {
	rootDir := t.TempDir()

	cfg, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    simple = {
      source = "hashicorp/test"
    }
  }
}

provider "simple" {}

step "example" {
  action "simple_action" "first" {
    config {
      value = "one"
    }
  }

  action "simple_action" "second" {
    config {
      value = "two"
    }
  }

  output "result" {
    value = true
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	config := &Config{RootPath: rootDir, Files: map[string]*File{cfg.Path: cfg}, Runbook: cfg.Runbook}
	bundle, diags := LowerStep(config, cfg.Steps["example"])
	tfdiags.AssertNoDiagnostics(t, diags)

	entries := bundle.SourceMaps["main.tf"]
	if len(entries) < 3 {
		t.Fatalf("expected source maps for appended blocks, got %#v", entries)
	}
	mainSrc := string(bundle.Files["main.tf"])
	if !strings.Contains(mainSrc, `action "simple_action" "first"`) || !strings.Contains(mainSrc, `action "simple_action" "second"`) {
		t.Fatalf("unexpected lowered main.tf:\n%s", mainSrc)
	}
}

func TestLowerStepWithScopeRewritesRunbookReferences(t *testing.T) {
	rootDir := t.TempDir()

	cfg, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    simple = {
      source = "hashicorp/test"
    }
  }
}

provider "simple" {}

step "example" {
  data "simple_resource" "current" {
    value = "hello"
  }

  precondition {
    condition     = workspace.output.enabled && steps.bootstrap.ready
    error_message = "bootstrap must run first"
  }

  action "simple_action" "target" {
    config {
      value = workspace.output.enabled ? data.simple_resource.current.value : steps.bootstrap.message
    }
  }

  output "result" {
    value = workspace.output.enabled && data.simple_resource.current.value == steps.bootstrap.message
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	config := &Config{RootPath: rootDir, Files: map[string]*File{cfg.Path: cfg}, Runbook: cfg.Runbook}
	bundle, diags := LowerStepWithScope(config, cfg.Steps["example"], EvalScope{
		Workspace: cty.ObjectVal(map[string]cty.Value{"output": cty.ObjectVal(map[string]cty.Value{"enabled": cty.True})}),
		Steps:     cty.ObjectVal(map[string]cty.Value{"bootstrap": cty.ObjectVal(map[string]cty.Value{"ready": cty.True, "message": cty.StringVal("hello")})}),
	})
	tfdiags.AssertNoDiagnostics(t, diags)

	mainSrc := string(bundle.Files["main.tf"])
	if strings.Contains(mainSrc, `= workspace.output.enabled`) || strings.Contains(mainSrc, `= steps.bootstrap.ready`) || strings.Contains(mainSrc, `= steps.bootstrap.message`) {
		t.Fatalf("lowered main.tf still contains runbook-only references:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `var.__runbook_workspace.output.enabled`) || !strings.Contains(mainSrc, `var.__runbook_steps.bootstrap.ready`) {
		t.Fatalf("lowered main.tf did not rewrite runbook references:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `output "__runbook_precondition_0_condition"`) {
		t.Fatalf("lowered main.tf did not preserve condition outputs:\n%s", mainSrc)
	}
}

func TestLowerStepWithScopePreservesStepLocals(t *testing.T) {
	rootDir := t.TempDir()

	cfg, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "example" {
  locals {
    summary_target = steps.bootstrap.message
  }

  output "summary_target" {
    value = local.summary_target
  }

  precondition {
    condition     = local.summary_target != ""
    error_message = "summary target must be set"
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	config := &Config{RootPath: rootDir, Files: map[string]*File{cfg.Path: cfg}, Runbook: cfg.Runbook}
	bundle, diags := LowerStepWithScope(config, cfg.Steps["example"], EvalScope{
		Steps: cty.ObjectVal(map[string]cty.Value{"bootstrap": cty.ObjectVal(map[string]cty.Value{"message": cty.StringVal("hello")})}),
	})
	tfdiags.AssertNoDiagnostics(t, diags)

	mainSrc := string(bundle.Files["main.tf"])
	if !strings.Contains(mainSrc, "locals {") {
		t.Fatalf("lowered main.tf did not emit locals block:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `summary_target = var.__runbook_steps.bootstrap.message`) {
		t.Fatalf("lowered main.tf did not rewrite local expression:\n%s", mainSrc)
	}
}

func TestLowerStepWithScopeRewritesObjectEachValueAccess(t *testing.T) {
	rootDir := t.TempDir()

	cfg, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "example" {
  for_each = {
    primary = {
      name = "runbook-scratchpad-ops-smoke"
      arn  = "arn:example"
    }
  }

  output "target" {
    value = each.value.name
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	config := &Config{RootPath: rootDir, Files: map[string]*File{cfg.Path: cfg}, Runbook: cfg.Runbook}
	bundle, diags := LowerStepInstanceWithScope(config, cfg.Steps["example"], EvalScope{
		Each: cty.ObjectVal(map[string]cty.Value{
			"key":   cty.StringVal("primary"),
			"value": cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("runbook-scratchpad-ops-smoke"), "arn": cty.StringVal("arn:example")}),
		}),
	})
	tfdiags.AssertNoDiagnostics(t, diags)

	mainSrc := string(bundle.Files["main.tf"])
	if strings.Contains(mainSrc, "null.name") {
		t.Fatalf("lowered main.tf rewrote each.value.name to null:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `runbook-scratchpad-ops-smoke`) || !strings.Contains(mainSrc, `arn:example`) || !strings.Contains(mainSrc, `}.name`) {
		t.Fatalf("lowered main.tf did not preserve object-valued each.value access:\n%s", mainSrc)
	}
}

func TestLowerStepWithScopeRewritesActionOutputToSyntheticStringVar(t *testing.T) {
	rootDir := t.TempDir()

	cfg, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "example" {
  postcondition {
    condition     = regex("(?s)\\[.*\\]", trimspace(action.aws_lambda_invoke.smoke.output)) != ""
    error_message = "must emit json"
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	config := &Config{RootPath: rootDir, Files: map[string]*File{cfg.Path: cfg}, Runbook: cfg.Runbook}
	bundle, diags := LowerStepWithScope(config, cfg.Steps["example"], EvalScope{})
	tfdiags.AssertNoDiagnostics(t, diags)

	mainSrc := string(bundle.Files["main.tf"])
	if strings.Contains(mainSrc, "__runbook_actions") {
		t.Fatalf("lowered main.tf still uses action object scope for output:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `var.__runbook_action_output__aws_lambda_invoke__smoke`) {
		t.Fatalf("lowered main.tf did not rewrite action output to synthetic string var:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `variable "__runbook_action_output__aws_lambda_invoke__smoke"`) {
		t.Fatalf("lowered main.tf did not declare synthetic action output variable:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `type = string`) {
		t.Fatalf("lowered main.tf did not type synthetic action output variable as string:\n%s", mainSrc)
	}
}

func TestLowerStepWithScopePreservesUnknownStepOutputsInDefaults(t *testing.T) {
	rootDir := t.TempDir()

	file, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "dependent" {
  output "seen_seed_output" {
    value = steps.bootstrap.seed_output
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	cfg := &Config{RootPath: rootDir, Files: map[string]*File{file.Path: file}, Runbook: file.Runbook}
	bundle, diags := LowerStepWithScope(cfg, file.Steps["dependent"], EvalScope{Steps: cty.ObjectVal(map[string]cty.Value{
		"bootstrap": cty.ObjectVal(map[string]cty.Value{
			"seed_output": cty.UnknownVal(cty.String),
		}),
	})})
	tfdiags.AssertNoDiagnostics(t, diags)

	mainSrc := string(bundle.Files["main.tf"])
	if !strings.Contains(mainSrc, `variable "__runbook_steps"`) {
		t.Fatalf("missing synthetic steps variable in lowered main.tf:\n%s", mainSrc)
	}
	if strings.Contains(mainSrc, `seed_output = null`) {
		t.Fatalf("unknown step output collapsed to null in lowered main.tf:\n%s", mainSrc)
	}
}
