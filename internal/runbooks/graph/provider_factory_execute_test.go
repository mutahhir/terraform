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
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/spf13/afero"
)

// providerProbe is a test helper that records every provider instance handed
// out by its factory, so a test can distinguish fresh per-step instances from a
// single shared instance reused across parallel steps.
type providerProbe struct {
	mu      sync.Mutex
	created []*testing_provider.MockProvider
}

func (p *providerProbe) factory() (providers.Interface, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	mp := &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: &configschema.Block{}},
			Actions: map[string]providers.ActionSchema{
				"test_action": {ConfigSchema: &configschema.Block{}},
			},
		},
	}
	p.created = append(p.created, mp)
	return mp, nil
}

func (p *providerProbe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.created)
}

func (p *providerProbe) instancesFrom(start int) []*testing_provider.MockProvider {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*testing_provider.MockProvider(nil), p.created[start:]...)
}

// loadTwoParallelInvokeStepsConfig builds a runbook with two independent steps,
// each invoking a provider action. Because the steps have no dependency between
// them, they execute in parallel and must each receive an independent provider
// instance.
func loadTwoParallelInvokeStepsConfig(t *testing.T) *runbookconfigs.RunbookConfig {
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

step "alpha" {
  action "test_action" "notify" {
    config {}
  }

  execute {
    invoke_action {
      action = action.test_action.notify
    }
  }
}

step "beta" {
  action "test_action" "notify" {
    config {}
  }

  execute {
    invoke_action {
      action = action.test_action.notify
    }
  }
}
`)
	return loadIntegrationRunbookConfig(t, fs)
}

// TestSavedPlanExecuteUsesFreshProviderInstancesPerStep is the regression test
// for hc-terraform-bd0.11. The saved-plan execute path
// (ImportSavedPlan -> ExecutePlan) must instantiate a fresh provider instance
// per step/provider-type at execute time, exactly like the in-process
// plan->execute path, rather than serializing all parallel steps on a single
// shared provider instance.
//
// Before the fix, ImportSavedPlan registered only shared provider instances and
// ExecutePlan ignored ExecuteOpts.Providers, so runbookProviderFresh fell back
// to the single shared instance. This test fails in that state because no fresh
// instances are minted during execute.
func TestSavedPlanExecuteUsesFreshProviderInstancesPerStep(t *testing.T) {
	config := loadTwoParallelInvokeStepsConfig(t)

	probe := &providerProbe{}
	providerMap := map[addrs.Provider]providers.Factory{
		addrs.NewDefaultProvider("test"): probe.factory,
	}

	// Produce a plan from source and serialize it, mirroring `runbook plan`.
	plan, planDiags := BuildPlan(config, &PlannerOpts{Providers: providerMap})
	if planDiags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}
	saved, err := ExportSavedPlan(plan, map[string][]byte{}, terraform.InputValues{})
	if err != nil {
		t.Fatalf("unexpected export error: %s", err)
	}

	// Rehydrate the saved plan in a fresh context, mirroring
	// `runbook execute <plan>`.
	importedPlan, importDiags := ImportSavedPlan(config, saved, nil, providerMap)
	if importDiags.HasErrors() {
		t.Fatalf("unexpected import diagnostics: %s", importDiags.Err())
	}

	before := probe.count()
	execDiags := ExecutePlan(importedPlan, &ExecuteOpts{Providers: providerMap})
	if execDiags.HasErrors() {
		t.Fatalf("unexpected execute diagnostics: %s", execDiags.Err())
	}

	assertFreshPerStepExecution(t, probe, before)
}

// TestInProcessExecuteUsesFreshProviderInstancesPerStep guards against a
// regression on the in-process plan->execute path (BuildPlan -> ExecutePlan
// without a saved-plan round trip), which must keep minting fresh per-step
// provider instances.
func TestInProcessExecuteUsesFreshProviderInstancesPerStep(t *testing.T) {
	config := loadTwoParallelInvokeStepsConfig(t)

	probe := &providerProbe{}
	providerMap := map[addrs.Provider]providers.Factory{
		addrs.NewDefaultProvider("test"): probe.factory,
	}

	plan, planDiags := BuildPlan(config, &PlannerOpts{Providers: providerMap})
	if planDiags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}

	before := probe.count()
	execDiags := ExecutePlan(plan, &ExecuteOpts{Providers: providerMap})
	if execDiags.HasErrors() {
		t.Fatalf("unexpected execute diagnostics: %s", execDiags.Err())
	}

	assertFreshPerStepExecution(t, probe, before)
}

func assertFreshPerStepExecution(t *testing.T, probe *providerProbe, before int) {
	t.Helper()
	fresh := probe.instancesFrom(before)
	if len(fresh) == 0 {
		t.Fatalf("expected execute to mint fresh provider instances per step from the factory; got none, meaning execute fell back to a shared provider instance")
	}

	invokedFresh := 0
	for _, p := range fresh {
		if p.InvokeActionCalled {
			invokedFresh++
		}
	}
	if invokedFresh != 2 {
		t.Fatalf("expected 2 fresh provider instances (one per parallel step) to invoke the action, got %d (created %d fresh instances during execute)", invokedFresh, len(fresh))
	}

	// The most recently created shared instance (registered by the producer)
	// must not have been used to invoke actions; execute must use the fresh
	// per-step instances instead.
	if before > 0 {
		shared := probe.instancesFrom(before - 1)[0]
		if shared.InvokeActionCalled {
			t.Fatalf("expected the shared producer-time provider instance not to invoke actions; execute should use fresh per-step instances")
		}
	}
}
