// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"sync"
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/providers"
	testing_provider "github.com/hashicorp/terraform/internal/providers/testing"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/spf13/afero"
)

// recordingHook captures every ActionExecEvent emitted during a runbook walk so
// a test can assert which actions surfaced events (vs being silently drained).
type recordingHook struct {
	mu     sync.Mutex
	events []ActionExecEvent
}

func (h *recordingHook) record(e ActionExecEvent) (HookAction, error) {
	h.mu.Lock()
	h.events = append(h.events, e)
	h.mu.Unlock()
	return HookActionContinue, nil
}

func (h *recordingHook) actionEventsForStep(stepName string) []ActionExecEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	var ret []ActionExecEvent
	for _, e := range h.events {
		if e.StepName == stepName {
			ret = append(ret, e)
		}
	}
	return ret
}

func (h *recordingHook) PlannedStep(*runbookruntime.Step) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *recordingHook) PlannedStepInfo(StepPlanInfo) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *recordingHook) ExecutingStep(*runbookruntime.Step) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *recordingHook) ExecutedStep(*runbookruntime.Step) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *recordingHook) ActionEvent(e ActionExecEvent) (HookAction, error) { return h.record(e) }
func (h *recordingHook) CatchTriggered(string, string) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *recordingHook) CatchCompleted(string, string) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *recordingHook) CatchSkipped(string, string) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *recordingHook) CatchFailed(string, string) (HookAction, error) {
	return HookActionContinue, nil
}

func loadFailingStepWithCatchAllConfig(t *testing.T) *runbookconfigs.RunbookConfig {
	t.Helper()
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

variable "fail" {
  type    = bool
  default = true
}

step "deploy" {
  action "test_action" "push" {
    config {}
  }

  execute {
    invoke_action {
      action = action.test_action.push
    }
  }

  postcondition {
    condition     = !var.fail
    error_message = "deploy failed"
  }
}

catch "notify" {
  action "test_action" "alert" {
    config {}
  }

  execute {
    invoke_action {
      action = action.test_action.alert
    }
  }
}
`)
	return loadIntegrationRunbookConfig(t, fs)
}

// TestCatchAllActionSurfacesEvents is the regression test for hc-terraform-81y:
// a catch-all (no precondition) whose action's only observable effect is its
// invocation events must surface those events, not silently drain them. Before
// the fix, executeCatchInvokeAction discarded resp.Events, so the catch action
// appeared to "not trigger". We assert the catch action emits ActionExecEvents
// under StepName "catch.notify".
func TestCatchAllActionSurfacesEvents(t *testing.T) {
	config := loadFailingStepWithCatchAllConfig(t)

	probe := func() (providers.Interface, error) {
		return &testing_provider.MockProvider{
			GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
				Provider: providers.Schema{Body: &configschema.Block{}},
				Actions: map[string]providers.ActionSchema{
					"test_action": {ConfigSchema: &configschema.Block{}},
				},
			},
		}, nil
	}
	providerMap := map[addrs.Provider]providers.Factory{
		addrs.NewDefaultProvider("test"): probe,
	}

	hook := &recordingHook{}
	plan, planDiags := BuildPlan(config, &PlannerOpts{Providers: providerMap})
	if planDiags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}

	// Execute is expected to surface the failing postcondition as an error.
	_ = ExecutePlan(plan, &ExecuteOpts{Providers: providerMap, Hooks: []Hook{hook}})

	catchEvents := hook.actionEventsForStep("catch.notify")
	if len(catchEvents) == 0 {
		t.Fatal("catch-all action emitted no ActionExecEvents: events were drained (hc-terraform-81y)")
	}
	sawCompleted := false
	for _, e := range catchEvents {
		if e.Status == "completed" {
			sawCompleted = true
		}
	}
	if !sawCompleted {
		t.Fatalf("expected a 'completed' ActionExecEvent for the catch action, got %+v", catchEvents)
	}
}
