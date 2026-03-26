// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package rpcapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/hashicorp/terraform-svchost/disco"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/depsfile"
	"github.com/hashicorp/terraform/internal/providercache"
	"github.com/hashicorp/terraform/internal/providers"
	provider_testing "github.com/hashicorp/terraform/internal/providers/testing"
	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/runbooks"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
	ctymsgpack "github.com/zclconf/go-cty/cty/msgpack"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRunbooksOpenCloseConfiguration(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles, disco.New())

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"
}

provider "aws" {}

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
    value = action.aws_lambda_invoke.foo.result.status_code
  }

  postcondition {
    condition     = action.aws_lambda_invoke.foo.result.status_code == 200
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
	server := newRunbooksServer(handles, disco.New())

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"
}

provider "aws" {}

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
    value = action.aws_lambda_invoke.foo.result.status_code
  }

  postcondition {
    condition     = action.aws_lambda_invoke.foo.result.status_code == 200
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
	if got, want := len(stepsResp.Config.Providers), 0; got != want {
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
	server := newRunbooksServer(handles, disco.New())
	server.providerCacheOverride = map[addrs.Provider]providers.Factory{
		addrs.NewDefaultProvider("test"): fixedMockProviderFactory(testRunbookProvider()),
	}
	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

step "first" {
  list "test_resource" "inventory" {
    provider = test

    config {
      filter = {
        attr = "hello"
      }
    }

    include_resource = true
    limit            = 10
  }

	  action "test_action" "target" {
	    config {
	      attr = "hello"
	    }
	  }

	  execute {
	    action_invoke {
	      action = action.test_action.target
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
	if got, want := resp.PlannedActions[0].Address, "action.test_action.target"; got != want {
		t.Fatalf("wrong planned action address: got %q want %q", got, want)
	}
	if got, want := len(resp.GetPlannedQueries()), 1; got != want {
		t.Fatalf("wrong planned query count: got %d want %d; status=%v detail=%q diagnostics=%v", got, want, resp.Status, resp.Detail, resp.Diagnostics)
	}
	if got, want := resp.GetPlannedQueries()[0].Address, `list.test_resource.inventory`; got != want {
		t.Fatalf("wrong planned query address: got %q want %q", got, want)
	}
	if got := resp.GetPlannedQueries()[0].GetData(); got == nil || len(got.Msgpack) == 0 {
		t.Fatalf("expected planned query data to be populated, got %#v", got)
	}
	if got, want := len(resp.LoweredFiles), 2; got != want {
		t.Fatalf("wrong lowered file count: got %d want %d", got, want)
	}
	paths := []string{resp.LoweredFiles[0].Path, resp.LoweredFiles[1].Path}
	if !(paths[0] == "main.tf" && paths[1] == "main.tfquery.hcl" || paths[0] == "main.tfquery.hcl" && paths[1] == "main.tf") {
		t.Fatalf("wrong lowered file paths: got %v", paths)
	}
}

func TestRunbooksPlanRunbookStepIncludesPlannedOutputsFromQuery(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles, disco.New())
	server.providerCacheOverride = map[addrs.Provider]providers.Factory{
		addrs.NewDefaultProvider("test"): fixedMockProviderFactory(testRunbookProvider()),
	}

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

step "first" {
  list "test_resource" "inventory" {
    provider = test

    config {
      filter = {
        attr = "hello"
      }
    }

    include_resource = true
    limit            = 10
  }

  output "ids" {
    value = [for item in list.test_resource.inventory.data : item.identity.id]
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
	planned := resp.GetPlannedOutputs()["ids"]
	if planned == nil {
		t.Fatal("expected planned output value for ids")
	}
	val, err := ctymsgpack.Unmarshal(planned.Msgpack, cty.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := val.LengthInt(), 1; got != want {
		t.Fatalf("wrong planned output length: got %d want %d", got, want)
	}
	item := val.Index(cty.NumberIntVal(0))
	if item.Type().IsObjectType() && item.Type().HasAttribute("identity") {
		item = item.GetAttr("identity").GetAttr("id")
	}
	if got, want := item.AsString(), "i-1"; got != want {
		t.Fatalf("wrong planned output item: got %q want %q", got, want)
	}
}

func TestRunbooksPlanRunbookStepProviderFunctionInOutput(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles, disco.New())
	server.providerCacheOverride = map[addrs.Provider]providers.Factory{
		addrs.NewDefaultProvider("test"): fixedMockProviderFactory(testRunbookProvider()),
	}

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

variable "name" {
  default = "hello"
}

step "first" {
  output "echoed" {
    value = provider::test::echo(var.name)
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
	planned := resp.GetPlannedOutputs()["echoed"]
	if planned == nil {
		t.Fatal("expected planned output value for echoed")
	}
	val, err := ctymsgpack.Unmarshal(planned.Msgpack, cty.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}
	if val.Type() != cty.String {
		t.Fatalf("wrong planned provider function output type: %s (%s)", val.Type().FriendlyName(), val.GoString())
	}
	if got, want := val.AsString(), "hello"; got != want {
		t.Fatalf("wrong planned provider function output: got %q want %q", got, want)
	}
}

func TestRunbooksPlanRunbookStepResolvesRootAction(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles, disco.New())

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

action "test_action" "root_target" {
  config {
    attr = "hello"
  }
}

step "first" {
  execute {
    action_invoke {
      action = action.test_action.root_target
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
	if got, want := resp.PlannedActions[0].Address, "action.test_action.root_target"; got != want {
		t.Fatalf("wrong planned action address: got %q want %q", got, want)
	}
}

func TestRunbooksPlanRunbookStepResolvesWorkspaceAction(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles, disco.New())

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tf"), []byte(`terraform {
  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

action "test_action" "workspace_target" {
  config {
    attr = "hello"
  }
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

step "first" {
  execute {
    action_invoke {
      action = workspace.action.test_action.workspace_target
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
	if got, want := resp.PlannedActions[0].Address, "workspace.action.test_action.workspace_target"; got != want {
		t.Fatalf("wrong planned action address: got %q want %q", got, want)
	}
}

func TestRunbooksPlanRunbookStepSkippedByPrecondition(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles, disco.New())
	server.providerCacheOverride = map[addrs.Provider]providers.Factory{
		addrs.NewDefaultProvider("aws"): fixedMockProviderFactory(testRunbookProvider()),
	}

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "first" {
	list "test_resource" "unmanaged" {
		provider = aws

    config {}

    include_resource = true
    limit            = 25
  }

  precondition {
		condition     = length(list.test_resource.unmanaged.data) > local.threshold
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

	if got, want := resp.Status, runbooks.StepStatus_STEP_STATUS_FAILED; got != want {
		t.Fatalf("wrong plan status: got %v want %v; diagnostics=%v", got, want, resp.Diagnostics)
	}
}

func TestRunbooksPlanRunbookStepRejectsUnsupportedListType(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles, disco.New())
	server.providerCacheOverride = map[addrs.Provider]providers.Factory{
		addrs.NewDefaultProvider("test"): fixedMockProviderFactory(testRunbookProvider()),
	}

	configPath := t.TempDir()
	locks := handles.NewDependencyLocks(depsfile.NewLocks())
	cache := handles.NewProviderPluginCache(providercache.NewDir(configPath))
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

step "discover" {
  list "wrong_list" "items" {
    provider = test

    config {
      filter = {
        attr = "x"
      }
    }
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
	runtimeResp, err := server.OpenRunbookRuntime(ctx, &runbooks.OpenRunbookRuntime_Request{
		DependencyLocksHandle: locks.ForProtobuf(),
		ProviderCacheHandle:   cache.ForProtobuf(),
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := server.PlanRunbookStep(ctx, &runbooks.PlanRunbookStep_Request{
		RunbookConfigHandle:  openResp.RunbookConfigHandle,
		RunbookRuntimeHandle: runtimeResp.RunbookRuntimeHandle,
		StepName:             "discover",
		Scope:                &runbooks.EvalScope{Variables: &runbooks.DynamicValue{Msgpack: mustMsgpackValue(t, cty.EmptyObjectVal)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resp.Status, runbooks.StepStatus_STEP_STATUS_FAILED; got != want {
		t.Fatalf("wrong status: got %v want %v", got, want)
	}
	if len(resp.Diagnostics) == 0 {
		t.Fatal("expected diagnostics")
	}
	if got := resp.Diagnostics[0].Summary; got != "Unsupported list resource type" {
		t.Fatalf("wrong diagnostic summary: %q", got)
	}
}

func TestRunbooksPlanRunbookStepRejectsInvalidActionConfig(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles, disco.New())
	server.providerCacheOverride = map[addrs.Provider]providers.Factory{
		addrs.NewDefaultProvider("test"): fixedMockProviderFactory(testRunbookProvider()),
	}

	configPath := t.TempDir()
	locks := handles.NewDependencyLocks(depsfile.NewLocks())
	cache := handles.NewProviderPluginCache(providercache.NewDir(configPath))
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

step "invoke" {
  list "test_resource" "inventory" {
    provider = test

    config {
      filter = {
        attr = "x"
      }
    }
  }

  action "test_action" "broken" {
    config {
      unknown_attr = "value"
    }
  }

  execute {
    action_invoke {
      action = action.test_action.broken
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
	runtimeResp, err := server.OpenRunbookRuntime(ctx, &runbooks.OpenRunbookRuntime_Request{
		DependencyLocksHandle: locks.ForProtobuf(),
		ProviderCacheHandle:   cache.ForProtobuf(),
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := server.PlanRunbookStep(ctx, &runbooks.PlanRunbookStep_Request{
		RunbookConfigHandle:  openResp.RunbookConfigHandle,
		RunbookRuntimeHandle: runtimeResp.RunbookRuntimeHandle,
		StepName:             "invoke",
		Scope:                &runbooks.EvalScope{Variables: &runbooks.DynamicValue{Msgpack: mustMsgpackValue(t, cty.EmptyObjectVal)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resp.Status, runbooks.StepStatus_STEP_STATUS_FAILED; got != want {
		t.Fatalf("wrong status: got %v want %v; diagnostics=%v", got, want, resp.Diagnostics)
	}
	if len(resp.Diagnostics) == 0 {
		t.Fatal("expected diagnostics")
	}
	if got := resp.Diagnostics[0].Summary; got != "Unsupported argument" {
		t.Fatalf("wrong diagnostic summary: %q", got)
	}
}

func TestRunbooksPlanRunbookStepRejectsPlanTimeActionErrorOnActionOnlyStep(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles, disco.New())
	provider := testRunbookProvider()
	provider.PlanActionFn = func(req providers.PlanActionRequest) providers.PlanActionResponse {
		var resp providers.PlanActionResponse
		resp.Diagnostics = resp.Diagnostics.Append(tfdiags.Sourceless(tfdiags.Error, "Invalid action plan", "step-local action failed during plan"))
		return resp
	}
	server.providerCacheOverride = map[addrs.Provider]providers.Factory{
		addrs.NewDefaultProvider("test"): fixedMockProviderFactory(provider),
	}

	configPath := t.TempDir()
	locks := handles.NewDependencyLocks(depsfile.NewLocks())
	cache := handles.NewProviderPluginCache(providercache.NewDir(configPath))
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

step "invoke" {
  action "test_action" "broken" {
    config {
      attr = "value"
    }
  }

  execute {
    action_invoke {
      action = action.test_action.broken
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
	runtimeResp, err := server.OpenRunbookRuntime(ctx, &runbooks.OpenRunbookRuntime_Request{
		DependencyLocksHandle: locks.ForProtobuf(),
		ProviderCacheHandle:   cache.ForProtobuf(),
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := server.PlanRunbookStep(ctx, &runbooks.PlanRunbookStep_Request{
		RunbookConfigHandle:  openResp.RunbookConfigHandle,
		RunbookRuntimeHandle: runtimeResp.RunbookRuntimeHandle,
		StepName:             "invoke",
		Scope:                &runbooks.EvalScope{Variables: &runbooks.DynamicValue{Msgpack: mustMsgpackValue(t, cty.EmptyObjectVal)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resp.Status, runbooks.StepStatus_STEP_STATUS_FAILED; got != want {
		t.Fatalf("wrong status: got %v want %v; diagnostics=%v", got, want, resp.Diagnostics)
	}
	if len(resp.Diagnostics) == 0 {
		t.Fatal("expected diagnostics")
	}
	if got := resp.Diagnostics[0].Summary; got != "Invalid action plan" {
		t.Fatalf("wrong diagnostic summary: %q", got)
	}
}

func TestRunbooksPlanRunbookStepUsesWorkspaceOutputScope(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles, disco.New())

	configPath := t.TempDir()
	err := os.WriteFile(filepath.Join(configPath, "main.tfrun.hcl"), []byte(`runbook {
  terraform_version = ">= 1.0.0"
}

step "first" {
  precondition {
    condition     = workspace.output.enabled
    error_message = "workspace output must enable this step"
    on_fail       = "skip"
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
			Workspace: &runbooks.DynamicValue{Msgpack: mustMsgpackValue(t, cty.ObjectVal(map[string]cty.Value{
				"output": cty.ObjectVal(map[string]cty.Value{
					"enabled": cty.True,
				}),
			}))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resp.Status, runbooks.StepStatus_STEP_STATUS_READY; got != want {
		t.Fatalf("wrong status: got %v want %v; diagnostics=%v", got, want, resp.Diagnostics)
	}
}

func TestRunbooksExecuteRunbookStepPostconditionFailure(t *testing.T) {
	ctx := context.Background()
	handles := newHandleTable()
	server := newRunbooksServer(handles, disco.New())

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
    condition     = action.aws_lambda_invoke.foo.result.status_code == 200
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
	server := newRunbooksServer(handles, disco.New())

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

func fixedMockProviderFactory(provider providers.Interface) providers.Factory {
	return func() (providers.Interface, error) {
		return provider, nil
	}
}

func testRunbookProvider() *provider_testing.MockProvider {
	provider := new(provider_testing.MockProvider)
	provider.GetProviderSchemaResponse = &providers.GetProviderSchemaResponse{
		Provider: providers.Schema{Body: &configschema.Block{}},
		Functions: map[string]providers.FunctionDecl{
			"echo": {
				Parameters: []providers.FunctionParam{{
					Name: "arg",
					Type: cty.String,
				}},
				ReturnType: cty.String,
			},
		},
		ResourceTypes: map[string]providers.Schema{
			"test_resource": {
				Body: &configschema.Block{
					Attributes: map[string]*configschema.Attribute{
						"id": {
							Type:     cty.String,
							Computed: true,
						},
						"instance_type": {
							Type:     cty.String,
							Computed: true,
						},
					},
				},
				Identity: &configschema.Object{
					Nesting: configschema.NestingSingle,
					Attributes: map[string]*configschema.Attribute{
						"id": {
							Type:     cty.String,
							Required: true,
						},
					},
				},
			},
		},
		ListResourceTypes: map[string]providers.Schema{
			"test_resource": {
				Body: &configschema.Block{
					Attributes: map[string]*configschema.Attribute{
						"data": {
							Type:     cty.DynamicPseudoType,
							Computed: true,
						},
					},
					BlockTypes: map[string]*configschema.NestedBlock{
						"config": {
							Nesting: configschema.NestingSingle,
							Block: configschema.Block{
								Attributes: map[string]*configschema.Attribute{
									"filter": {
										Required: true,
										NestedType: &configschema.Object{
											Nesting: configschema.NestingSingle,
											Attributes: map[string]*configschema.Attribute{
												"attr": {
													Type:     cty.String,
													Required: true,
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		Actions: map[string]providers.ActionSchema{
			"test_action": {
				ConfigSchema: &configschema.Block{
					Attributes: map[string]*configschema.Attribute{
						"attr": {
							Type:     cty.String,
							Optional: true,
						},
					},
				},
			},
		},
	}
	provider.ListResourceFn = func(request providers.ListResourceRequest) providers.ListResourceResponse {
		result := map[string]cty.Value{
			"data": cty.TupleVal([]cty.Value{
				cty.ObjectVal(map[string]cty.Value{
					"identity": cty.ObjectVal(map[string]cty.Value{
						"id": cty.StringVal("i-1"),
					}),
					"display_name": cty.StringVal("Item 1"),
					"state": cty.ObjectVal(map[string]cty.Value{
						"id":            cty.StringVal("1"),
						"instance_type": cty.StringVal(fmt.Sprintf("from-%s", request.TypeName)),
					}),
				}),
			}),
		}
		for k, v := range request.Config.AsValueMap() {
			if k != "data" {
				result[k] = v
			}
		}
		return providers.ListResourceResponse{Result: cty.ObjectVal(result)}
	}
	provider.PlanActionFn = func(req providers.PlanActionRequest) providers.PlanActionResponse {
		return providers.PlanActionResponse{}
	}
	provider.CallFunctionFn = func(req providers.CallFunctionRequest) providers.CallFunctionResponse {
		return providers.CallFunctionResponse{Result: req.Arguments[0]}
	}
	return provider
}
