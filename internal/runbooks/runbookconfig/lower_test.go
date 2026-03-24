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

	config := &Config{
		RootPath: rootDir,
		Files: map[string]*File{
			cfg.Path: cfg,
		},
		Runbook: cfg.Runbook,
	}

	step := cfg.Steps["example"]
	if step == nil {
		t.Fatal("expected step to be present")
	}

	bundle, diags := LowerStep(config, step)
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
    aws = {
      source = "hashicorp/aws"
    }
  }
}

variable "aws_region" {
  type        = string
  description = "AWS region"
  default     = "us-east-1"
}

provider "aws" {
  region = var.aws_region
}

step "example" {
  list "aws_s3_bucket" "inventory" {
    provider = aws

    config {}
  }
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	config := &Config{
		RootPath: rootDir,
		Files: map[string]*File{
			cfg.Path: cfg,
		},
		Variables: cfg.Variables,
		Runbook:   cfg.Runbook,
	}

	step := cfg.Steps["example"]
	if step == nil {
		t.Fatal("expected step to be present")
	}

	bundle, diags := LowerStep(config, step)
	tfdiags.AssertNoDiagnostics(t, diags)

	mainSrc := string(bundle.Files["main.tf"])
	if !strings.Contains(mainSrc, `variable "aws_region"`) {
		t.Fatalf("lowered main.tf did not preserve variable block:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `description = "AWS region"`) {
		t.Fatalf("lowered main.tf did not preserve variable description:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `region = var.aws_region`) {
		t.Fatalf("lowered main.tf did not preserve provider variable reference:\n%s", mainSrc)
	}
}

func TestLowerStepPreservesVariableValidationBlocks(t *testing.T) {
	rootDir := t.TempDir()

	cfg, diags := ParseFileSource([]byte(`runbook {
  terraform_version = ">= 1.0.0"
}

variable "aws_region" {
  type        = string
  description = "AWS region"
  default     = "us-east-1"

  validation {
    condition     = length(var.aws_region) > 0
    error_message = "aws_region must not be empty"
  }
}

step "example" {
  execute {}
}
`), filepath.Join(rootDir, "main.tfrun.hcl"))
	tfdiags.AssertNoDiagnostics(t, diags)

	config := &Config{
		RootPath:  rootDir,
		Files:     map[string]*File{cfg.Path: cfg},
		Variables: cfg.Variables,
		Runbook:   cfg.Runbook,
	}

	bundle, diags := LowerStep(config, cfg.Steps["example"])
	tfdiags.AssertNoDiagnostics(t, diags)

	mainSrc := string(bundle.Files["main.tf"])
	if !strings.Contains(mainSrc, "validation {") {
		t.Fatalf("lowered main.tf did not preserve variable validation block:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `error_message = "aws_region must not be empty"`) {
		t.Fatalf("lowered main.tf did not preserve variable validation message:\n%s", mainSrc)
	}
}

func TestLowerStepPreservesStepDataBlocks(t *testing.T) {
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

	config := &Config{
		RootPath: rootDir,
		Files:    map[string]*File{cfg.Path: cfg},
		Runbook:  cfg.Runbook,
	}

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

	config := &Config{
		RootPath: rootDir,
		Files:    map[string]*File{cfg.Path: cfg},
		Runbook:  cfg.Runbook,
	}

	bundle, diags := LowerStepWithScope(config, cfg.Steps["example"], EvalScope{
		Workspace: cty.ObjectVal(map[string]cty.Value{
			"output": cty.ObjectVal(map[string]cty.Value{
				"enabled": cty.True,
			}),
		}),
		Steps: cty.ObjectVal(map[string]cty.Value{
			"bootstrap": cty.ObjectVal(map[string]cty.Value{
				"ready":   cty.True,
				"message": cty.StringVal("hello"),
			}),
		}),
	})
	tfdiags.AssertNoDiagnostics(t, diags)

	mainSrc := string(bundle.Files["main.tf"])
	if strings.Contains(mainSrc, `= workspace.output.enabled`) || strings.Contains(mainSrc, `= steps.bootstrap.ready`) || strings.Contains(mainSrc, `= steps.bootstrap.message`) {
		t.Fatalf("lowered main.tf still contains runbook-only references:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `var.__runbook_workspace.output.enabled`) {
		t.Fatalf("lowered main.tf did not rewrite workspace output reference:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `var.__runbook_steps.bootstrap.ready`) {
		t.Fatalf("lowered main.tf did not rewrite step reference:\n%s", mainSrc)
	}
	if strings.Contains(mainSrc, `value = workspace.output.enabled`) || strings.Contains(mainSrc, `value = steps.bootstrap.ready`) {
		t.Fatalf("lowered main.tf still contains unrewritten runbook expressions:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `data.simple_resource.current.value`) {
		t.Fatalf("lowered main.tf should preserve terraform-native data references:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `variable "__runbook_workspace"`) || !strings.Contains(mainSrc, `variable "__runbook_steps"`) {
		t.Fatalf("lowered main.tf did not emit synthetic runbook variables:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `output "result" {
  value     = var.__runbook_workspace.output.enabled && data.simple_resource.current.value == var.__runbook_steps.bootstrap.message
  sensitive = true
}`) {
		t.Fatalf("lowered main.tf did not mark rewritten step outputs as sensitive:\n%s", mainSrc)
	}
	if !strings.Contains(mainSrc, `output "__runbook_precondition_0_condition" {
  value     = var.__runbook_workspace.output.enabled && var.__runbook_steps.bootstrap.ready
  sensitive = true
}`) {
		t.Fatalf("lowered main.tf did not mark synthetic condition outputs as sensitive:\n%s", mainSrc)
	}
}
