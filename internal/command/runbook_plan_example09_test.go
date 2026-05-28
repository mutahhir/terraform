package command

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/providers"
	testing_provider "github.com/hashicorp/terraform/internal/providers/testing"
	"github.com/zclconf/go-cty/cty"
)

func TestRunbookPlanExample09ExecuteTimePreconditions(t *testing.T) {
	td, runbookDir := setupRunbookDir(t)
	writeFile(t, td+"/main.tf", ``)

	// Copy the example runbook config
	writeFile(t, filepath.Join(runbookDir, "main.tfrun.hcl"), `
runbook {
  terraform_version = ">= 1.14.0"
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {}

variable "app_version" {
  type    = string
  default = "1.0.0"
}

step "build" {
  action "aws_codebuild_start_build" "trigger" {
    config {
      project_name = "demo-build"
    }
  }

  execute {
    invoke_action {
      action = action.aws_codebuild_start_build.trigger
    }
  }

  output "build_id" {
    value = action.aws_codebuild_start_build.trigger.build_id
  }

  output "build_status" {
    value = action.aws_codebuild_start_build.trigger.build_status
  }

  output "build_succeeded" {
    value = action.aws_codebuild_start_build.trigger.build_status == "SUCCEEDED"
  }
}

step "deploy" {
  precondition {
    condition     = step.build.build_succeeded == true
    error_message = "Build failed. Cannot deploy."
    on_failure    = "skip"
  }

  action "aws_lambda_update_function_code" "deploy_api" {
    config {
      function_name = "demo-api"
      s3_bucket     = "demo-artifacts"
      s3_key        = "builds/${var.app_version}/api.zip"
    }
  }

  execute {
    invoke_action {
      action = action.aws_lambda_update_function_code.deploy_api
    }
  }

  output "deployed" {
    value = true
  }
}

step "notify" {
  precondition {
    condition     = step.deploy.deployed == true
    error_message = "Deploy was skipped."
    on_failure    = "skip"
  }

  action "aws_sns_publish" "deploy_complete" {
    config {
      topic_arn = "arn:aws:sns:us-east-1:123:topic"
      message   = "Deployed"
    }
  }

  execute {
    invoke_action {
      action = action.aws_sns_publish.deploy_complete
    }
  }

  output "notified" {
    value = true
  }
}

output "build_status" {
  value = step.build.build_status
}

output "deployed" {
  value = step.deploy.deployed
}
`)
	t.Chdir(runbookDir)

	provider := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"aws_codebuild_start_build":       {ConfigSchema: &configschema.Block{Attributes: map[string]*configschema.Attribute{"project_name": {Type: cty.String, Optional: true}}}},
				"aws_lambda_update_function_code": {ConfigSchema: &configschema.Block{Attributes: map[string]*configschema.Attribute{"function_name": {Type: cty.String, Optional: true}, "s3_bucket": {Type: cty.String, Optional: true}, "s3_key": {Type: cty.String, Optional: true}}}},
				"aws_sns_publish":                 {ConfigSchema: &configschema.Block{Attributes: map[string]*configschema.Attribute{"topic_arn": {Type: cty.String, Optional: true}, "message": {Type: cty.String, Optional: true}, "subject": {Type: cty.String, Optional: true}}}},
			},
		},
	}

	view, done := testView(t)
	c := &RunbookPlanCommand{runbookCommandBase: runbookCommandBase{Meta: Meta{View: view, testingOverrides: &testingOverrides{
		Providers: map[addrs.Provider]providers.Factory{
			addrs.NewDefaultProvider("aws"): providers.FactoryFixed(provider),
		},
	}}}}

	code := c.Run([]string{"-no-color"})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d:\nstderr: %s\nstdout: %s", code, output.Stderr(), output.Stdout())
	}
	stdout := output.Stdout()
	// All three steps should appear
	if !strings.Contains(stdout, "step.build") {
		t.Fatalf("expected step.build in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, "step.deploy") {
		t.Fatalf("expected step.deploy in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, "step.notify") {
		t.Fatalf("expected step.notify in output, got: %s", stdout)
	}
	// Deploy and notify should NOT be skipped (preconditions deferred)
	if strings.Contains(stdout, "step.deploy") && strings.Contains(stdout, "(skipped)") {
		t.Fatalf("step.deploy should not be skipped at plan time, got: %s", stdout)
	}
	// Should show 3 steps will execute
	if !strings.Contains(stdout, "3 step") {
		t.Fatalf("expected 3 steps in summary, got: %s", stdout)
	}
	t.Logf("Plan output:\n%s", stdout)
}
