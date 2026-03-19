// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package rpcapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/runbooks"
	"github.com/zclconf/go-cty/cty"
	ctymsgpack "github.com/zclconf/go-cty/cty/msgpack"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRunbooksOpenCloseConfiguration(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles)

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"

  provider "aws" {}
}

step "first" {
  precondition {
    condition     = var.search_count > 0
    error_message = "need search results"
    on_fail       = "skip"
  }

  config {}

  locals {
    threshold = 1
  }

  action "aws_lambda_invoke" "foo" {}

  list "aws_lambda" "unmanaged" {}

  execute {}

  output "status_code" {
    value = actions.aws_lambda_invoke.foo.result.status_code
  }

  postcondition {
    condition     = actions.aws_lambda_invoke.foo.result.status_code == 200
    error_message = "invoke failed"
  }
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	openResp, err := server.OpenRunbookConfiguration(ctx, &runbooks.OpenRunbookConfiguration_Request{
		ConfigPath: configPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if openResp.RunbookConfigHandle == 0 {
		t.Fatal("expected non-zero runbook config handle")
	}

	_, err = server.CloseRunbookConfiguration(ctx, &runbooks.CloseRunbookConfiguration_Request{
		RunbookConfigHandle: openResp.RunbookConfigHandle,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = server.CloseRunbookConfiguration(ctx, &runbooks.CloseRunbookConfiguration_Request{
		RunbookConfigHandle: openResp.RunbookConfigHandle,
	})
	if err == nil {
		t.Fatal("expected invalid handle error")
	}
	protoStatus, ok := status.FromError(err)
	if !ok {
		t.Fatal("error is not a protobuf status")
	}
	if diff := cmp.Diff(codes.InvalidArgument, protoStatus.Code()); diff != "" {
		t.Fatalf("wrong status code (-want +got):\n%s", diff)
	}
}

func TestRunbooksFindConfigurationSteps(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles)

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"

  provider "aws" {}
}

step "first" {
  precondition {
    condition     = var.search_count > 0
    error_message = "need search results"
    on_fail       = "skip"
  }

  config {}
  locals {
    threshold = 1
  }
  action "aws_lambda_invoke" "foo" {}
  list "aws_lambda" "unmanaged" {}
  execute {}

  output "status_code" {
    value = actions.aws_lambda_invoke.foo.result.status_code
  }

  postcondition {
    condition     = actions.aws_lambda_invoke.foo.result.status_code == 200
    error_message = "invoke failed"
  }
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(configPath, "vars.tfrun.hcl"), []byte(`variable "search_count" {
  default = 0
}

variable "lambda_name" {
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	openResp, err := server.OpenRunbookConfiguration(ctx, &runbooks.OpenRunbookConfiguration_Request{
		ConfigPath: configPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	stepsResp, err := server.FindRunbookConfigurationSteps(ctx, &runbooks.FindRunbookConfigurationSteps_Request{
		RunbookConfigHandle: openResp.RunbookConfigHandle,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := stepsResp.Config.TerraformVersion, ">= 1.0.0"; got != want {
		t.Fatalf("wrong terraform version: got %q want %q", got, want)
	}
	if got, want := len(stepsResp.Config.Providers), 1; got != want {
		t.Fatalf("wrong provider count: got %d want %d", got, want)
	}
	if got, want := len(stepsResp.Config.Variables), 2; got != want {
		t.Fatalf("wrong variable count: got %d want %d", got, want)
	}
	first := stepsResp.Config.Steps["first"]
	if first == nil {
		t.Fatal("expected top-level step 'first'")
	}
	if got, want := first.ActionCount, int64(1); got != want {
		t.Fatalf("wrong action count: got %d want %d", got, want)
	}
	if got, want := first.ListCount, int64(1); got != want {
		t.Fatalf("wrong list count: got %d want %d", got, want)
	}
	if got, want := first.ExecuteCount, int64(1); got != want {
		t.Fatalf("wrong execute count: got %d want %d", got, want)
	}
	if got, want := len(first.Preconditions), 1; got != want {
		t.Fatalf("wrong precondition count: got %d want %d", got, want)
	}
	if got, want := len(first.Postconditions), 1; got != want {
		t.Fatalf("wrong postcondition count: got %d want %d", got, want)
	}
	if got, want := first.Preconditions[0].OnFail, runbooks.FindRunbookConfigurationSteps_Condition_ON_FAIL_SKIP; got != want {
		t.Fatalf("wrong precondition on_fail: got %v want %v", got, want)
	}
	if got, want := len(first.Locals), 1; got != want {
		t.Fatalf("wrong locals count: got %d want %d", got, want)
	}
	if got, want := len(first.Outputs), 1; got != want {
		t.Fatalf("wrong outputs count: got %d want %d", got, want)
	}
}

func TestRunbooksPlanRunbookStepIncludesPlannedActions(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles)

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "first" {
  action "action_example" "target" {
    config {
      attr = "hello"
    }
  }

  execute {
    action_invoke {
      action = action.action_example.target
    }
  }
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	openResp, err := server.OpenRunbookConfiguration(ctx, &runbooks.OpenRunbookConfiguration_Request{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := server.PlanRunbookStep(ctx, &runbooks.PlanRunbookStep_Request{
		RunbookConfigHandle: openResp.RunbookConfigHandle,
		StepName:            "first",
		Scope:               &runbooks.EvalScope{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(resp.PlannedActions), 1; got != want {
		t.Fatalf("wrong planned action count: got %d want %d", got, want)
	}
	if got, want := resp.PlannedActions[0].Address, "action.action_example.target"; got != want {
		t.Fatalf("wrong planned action address: got %q want %q", got, want)
	}
	if got, want := len(resp.LoweredFiles), 1; got != want {
		t.Fatalf("wrong lowered file count: got %d want %d", got, want)
	}
	if got, want := resp.LoweredFiles[0].Path, "main.tf"; got != want {
		t.Fatalf("wrong lowered file path: got %q want %q", got, want)
	}
}

func TestRunbooksPlanRunbookStepSkippedByPrecondition(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles)

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "first" {
  list "aws_lambda" "unmanaged" {
    provider = aws

    config {}

    include_resource = true
    limit            = 25
  }

  precondition {
    condition     = length(list.aws_lambda.unmanaged.data) > local.threshold
    error_message = "need search results"
    on_fail       = "skip"
  }

  locals {
    threshold = 0
  }

  execute {}
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	openResp, err := server.OpenRunbookConfiguration(ctx, &runbooks.OpenRunbookConfiguration_Request{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := server.PlanRunbookStep(ctx, &runbooks.PlanRunbookStep_Request{
		RunbookConfigHandle: openResp.RunbookConfigHandle,
		StepName:            "first",
		Scope: &runbooks.EvalScope{
			Locals: &runbooks.DynamicValue{Msgpack: mustMsgpackValue(t, cty.ObjectVal(map[string]cty.Value{
				"threshold": cty.NumberIntVal(0),
			}))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := resp.Status, runbooks.StepStatus_STEP_STATUS_SKIPPED; got != want {
		t.Fatalf("wrong plan status: got %v want %v", got, want)
	}
}

func TestRunbooksExecuteRunbookStepPostconditionFailure(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles)

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "first" {
  precondition {
    condition     = var.search_count > steps.bootstrap.previous_count
    error_message = "need search results"
  }

  execute {}

  postcondition {
    condition     = actions.aws_lambda_invoke.foo.result.status_code == 200
    error_message = "invoke failed"
  }
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(configPath, "bootstrap.tfrun.hcl"), []byte(`step "bootstrap" {
  execute {}

  output "previous_count" {
    value = 0
  }
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	openResp, err := server.OpenRunbookConfiguration(ctx, &runbooks.OpenRunbookConfiguration_Request{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := server.ExecuteRunbookStep(ctx, &runbooks.ExecuteRunbookStep_Request{
		RunbookConfigHandle: openResp.RunbookConfigHandle,
		StepName:            "first",
		PreExecuteScope: &runbooks.EvalScope{
			Variables: &runbooks.DynamicValue{Msgpack: mustMsgpackValue(t, cty.ObjectVal(map[string]cty.Value{
				"search_count": cty.NumberIntVal(1),
			}))},
			Steps: &runbooks.DynamicValue{Msgpack: mustMsgpackValue(t, cty.ObjectVal(map[string]cty.Value{
				"bootstrap": cty.ObjectVal(map[string]cty.Value{
					"previous_count": cty.NumberIntVal(0),
				}),
			}))},
		},
		PostExecuteScope: &runbooks.EvalScope{
			Actions: &runbooks.DynamicValue{Msgpack: mustMsgpackValue(t, cty.ObjectVal(map[string]cty.Value{
				"aws_lambda_invoke": cty.ObjectVal(map[string]cty.Value{
					"foo": cty.ObjectVal(map[string]cty.Value{
						"result": cty.ObjectVal(map[string]cty.Value{
							"status_code": cty.NumberIntVal(500),
						}),
					}),
				}),
			}))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := resp.Status, runbooks.StepStatus_STEP_STATUS_FAILED; got != want {
		t.Fatalf("wrong execute status: got %v want %v", got, want)
	}
	if len(resp.Diagnostics) == 0 {
		t.Fatal("expected execute diagnostics")
	}
}

func TestRunbooksGetRunnableRunbookSteps(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles)

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "bootstrap" {
  execute {}

  output "count" {
    value = 1
  }
}

step "dependent" {
  precondition {
    condition     = steps.bootstrap.count > 0
    error_message = "bootstrap has not run"
  }

  execute {}
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	openResp, err := server.OpenRunbookConfiguration(ctx, &runbooks.OpenRunbookConfiguration_Request{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}

	readyResp, err := server.GetRunnableRunbookSteps(ctx, &runbooks.GetRunnableRunbookSteps_Request{
		RunbookConfigHandle: openResp.RunbookConfigHandle,
	})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"bootstrap"}, readyResp.StepNames); diff != "" {
		t.Fatalf("wrong initial runnable steps (-want +got):\n%s", diff)
	}

	readyResp, err = server.GetRunnableRunbookSteps(ctx, &runbooks.GetRunnableRunbookSteps_Request{
		RunbookConfigHandle: openResp.RunbookConfigHandle,
		CompletedSteps:      []string{"bootstrap"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"dependent"}, readyResp.StepNames); diff != "" {
		t.Fatalf("wrong runnable steps after bootstrap (-want +got):\n%s", diff)
	}
}

func mustMsgpackValue(t *testing.T, v cty.Value) []byte {
	t.Helper()
	ret, err := ctymsgpack.Marshal(v, cty.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}
	return ret
}
