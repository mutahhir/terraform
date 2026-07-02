// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/providers"
	testing_provider "github.com/hashicorp/terraform/internal/providers/testing"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
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

func (p *providerProbe) all() []*testing_provider.MockProvider {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*testing_provider.MockProvider(nil), p.created...)
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

// TestSavedPlanExecuteSharesPooledProviderInstance is the regression test for
// the provider pool (hc-terraform-wdc.2/wdc.3). The saved-plan execute path
// (ImportSavedPlan -> ExecutePlan) must reuse the single shared, pooled
// provider instance across all parallel steps — configured exactly once and
// closed at teardown — rather than minting (and leaking) a fresh provider
// instance per step.
func TestSavedPlanExecuteSharesPooledProviderInstance(t *testing.T) {
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

	assertPooledExecution(t, probe, before)
}

// TestInProcessExecuteSharesPooledProviderInstance guards the in-process
// plan->execute path (BuildPlan -> ExecutePlan without a saved-plan round trip):
// it must reuse the same pooled provider instance for both the plan and execute
// walks, configured exactly once and closed at teardown.
func TestInProcessExecuteSharesPooledProviderInstance(t *testing.T) {
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

	// The in-process path reuses the plan-time eval context, so execute mints no
	// new instances at all and shares the one created by the producer.
	if minted := probe.count() - before; minted != 0 {
		t.Fatalf("expected execute to mint no new provider instances (pool reuse); minted %d", minted)
	}
	assertPooledExecution(t, probe, before)
}

// assertPooledExecution verifies the pool semantics that replaced the previous
// fresh-per-step behavior: execute does not mint a new instance per step, a
// single shared instance invokes the parallel steps' actions, it is configured
// exactly once, and it is closed at teardown.
func assertPooledExecution(t *testing.T, probe *providerProbe, before int) {
	t.Helper()

	// Execute must not mint a fresh instance per step. At most the producer's
	// pre-registered instance is (re)used; no per-step instantiation.
	if minted := probe.count() - before; minted > 0 {
		t.Fatalf("expected execute to reuse the pooled provider instance, but %d fresh instances were minted during execute", minted)
	}

	// Exactly one instance should have been used to invoke the parallel steps'
	// actions, and it must have been configured exactly once and closed.
	invoked := 0
	var shared *testing_provider.MockProvider
	for _, p := range probe.all() {
		if p.InvokeActionCalled {
			invoked++
			shared = p
		}
	}
	if invoked != 1 {
		t.Fatalf("expected exactly one shared pooled instance to invoke actions, got %d", invoked)
	}
	if !shared.ConfigureProviderCalled {
		t.Fatalf("expected the shared pooled instance to have been configured")
	}
	if !shared.CloseCalled {
		t.Fatalf("expected the shared pooled instance to be closed at teardown (leak); CloseCalled was false")
	}
}

// TestProviderPoolConfiguresOnceUnderRace exercises the configure-once
// guarantee of the provider pool under the parallel graph walk. Two independent
// steps invoke actions concurrently against the single shared instance; the
// pool must call ConfigureProvider on that instance exactly once and never
// concurrently. Run under `-race` this also asserts there is no data race on
// the shared instance. (hc-terraform-wdc.3)
func TestProviderPoolConfiguresOnceUnderRace(t *testing.T) {
	config := loadTwoParallelInvokeStepsConfig(t)

	var (
		mu             sync.Mutex
		configureCount int
		inConfigure    int
		maxInConfigure int
	)
	makeProvider := func() *testing_provider.MockProvider {
		mp := &testing_provider.MockProvider{
			GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
				Provider: providers.Schema{Body: &configschema.Block{}},
				Actions: map[string]providers.ActionSchema{
					"test_action": {ConfigSchema: &configschema.Block{}},
				},
			},
		}
		mp.ConfigureProviderFn = func(providers.ConfigureProviderRequest) providers.ConfigureProviderResponse {
			mu.Lock()
			configureCount++
			inConfigure++
			if inConfigure > maxInConfigure {
				maxInConfigure = inConfigure
			}
			mu.Unlock()
			// Hold briefly to widen any concurrency window.
			time.Sleep(2 * time.Millisecond)
			mu.Lock()
			inConfigure--
			mu.Unlock()
			return providers.ConfigureProviderResponse{}
		}
		return mp
	}

	providerMap := map[addrs.Provider]providers.Factory{
		addrs.NewDefaultProvider("test"): func() (providers.Interface, error) {
			return makeProvider(), nil
		},
	}

	plan, planDiags := BuildPlan(config, &PlannerOpts{Providers: providerMap})
	if planDiags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}
	execDiags := ExecutePlan(plan, &ExecuteOpts{Providers: providerMap})
	if execDiags.HasErrors() {
		t.Fatalf("unexpected execute diagnostics: %s", execDiags.Err())
	}

	mu.Lock()
	defer mu.Unlock()
	if configureCount != 1 {
		t.Fatalf("expected ConfigureProvider to be called exactly once on the shared pooled instance, got %d", configureCount)
	}
	if maxInConfigure > 1 {
		t.Fatalf("expected no concurrent ConfigureProvider calls, observed %d concurrent", maxInConfigure)
	}
}

// loadTwoTypedParallelInvokeStepsConfig builds a runbook with two independent
// steps that invoke actions belonging to two DIFFERENT provider types (alpha,
// beta). Because each step talks to a different provider instance, their
// InvokeAction calls are not serialized by a single MockProvider's internal
// lock, so a test can observe whether the two steps' actions actually run
// concurrently on the parallel graph walk.
func loadTwoTypedParallelInvokeStepsConfig(t *testing.T) *runbookconfigs.RunbookConfig {
	t.Helper()
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    alpha = {
      source = "hashicorp/alpha"
    }
    beta = {
      source = "hashicorp/beta"
    }
  }
}

provider "alpha" {}
provider "beta" {}

step "alpha" {
  action "alpha_act" "notify" {
    config {}
  }

  execute {
    invoke_action {
      action = action.alpha_act.notify
    }
  }
}

step "beta" {
  action "beta_act" "notify" {
    config {}
  }

  execute {
    invoke_action {
      action = action.beta_act.notify
    }
  }
}
`)
	return loadIntegrationRunbookConfig(t, fs)
}

// TestTwoStepsWithActionsRunInParallel is the direct answer to "can two steps
// with actions run in parallel?". Two independent steps each invoke an action
// on a distinct provider type. Each action signals that it has entered
// InvokeAction and then blocks until the other has also entered (a rendezvous
// barrier). If the graph walk executed the steps serially, the first action
// would block forever and the test would hit its deadline; the barrier can only
// be satisfied when both actions are in-flight at the same time. This proves
// genuine wall-clock parallelism, not merely shared-instance reuse.
func TestTwoStepsWithActionsRunInParallel(t *testing.T) {
	config := loadTwoTypedParallelInvokeStepsConfig(t)

	const stepCount = 2
	var entered sync.WaitGroup
	entered.Add(stepCount)
	bothEntered := make(chan struct{})
	go func() {
		entered.Wait()
		close(bothEntered)
	}()

	var timedOut int32
	// barrierAction returns an InvokeAction implementation that records its
	// arrival and then waits for its sibling to arrive too (or times out).
	barrierAction := func(req providers.InvokeActionRequest) providers.InvokeActionResponse {
		entered.Done()
		select {
		case <-bothEntered:
			// Both steps' actions are in-flight simultaneously: parallel.
		case <-time.After(5 * time.Second):
			// Sibling never arrived while we held the floor: serialized.
			atomic.StoreInt32(&timedOut, 1)
		}
		return providers.InvokeActionResponse{}
	}

	newTypedProvider := func(actionType string) func() (providers.Interface, error) {
		return func() (providers.Interface, error) {
			mp := &testing_provider.MockProvider{
				GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
					Provider: providers.Schema{Body: &configschema.Block{}},
					Actions: map[string]providers.ActionSchema{
						actionType: {ConfigSchema: &configschema.Block{}},
					},
				},
			}
			mp.InvokeActionFn = barrierAction
			return mp, nil
		}
	}

	providerMap := map[addrs.Provider]providers.Factory{
		addrs.NewDefaultProvider("alpha"): newTypedProvider("alpha_act"),
		addrs.NewDefaultProvider("beta"):  newTypedProvider("beta_act"),
	}

	plan, planDiags := BuildPlan(config, &PlannerOpts{Providers: providerMap})
	if planDiags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}

	// Guard the whole execute with a deadline so a serialized walk surfaces as a
	// clear test failure rather than a hang.
	done := make(chan tfdiags.Diagnostics, 1)
	go func() {
		done <- ExecutePlan(plan, &ExecuteOpts{Providers: providerMap})
	}()
	select {
	case execDiags := <-done:
		if execDiags.HasErrors() {
			t.Fatalf("unexpected execute diagnostics: %s", execDiags.Err())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("ExecutePlan did not finish: steps did not run in parallel (rendezvous deadlocked)")
	}

	if atomic.LoadInt32(&timedOut) != 0 {
		t.Fatal("steps with actions did not run in parallel: an action timed out waiting for its sibling to start")
	}
}

// TestWalkSemaphoreBoundsConcurrency verifies the walk-level parallelism cap
// (hc-terraform-wdc.1): the semaphore that walkGraph/walkSubGraph acquire around
// each executable callback must never allow more than `limit` callbacks to run
// at once, and a non-positive limit must be unbounded.
func TestWalkSemaphoreBoundsConcurrency(t *testing.T) {
	for _, limit := range []int{1, 2, 5} {
		limit := limit
		t.Run(fmt.Sprintf("limit-%d", limit), func(t *testing.T) {
			sem := newWalkSemaphore(limit)
			var (
				mu       sync.Mutex
				inFlight int
				maxSeen  int
				wg       sync.WaitGroup
			)
			for i := 0; i < limit*8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					sem.Acquire()
					defer sem.Release()
					mu.Lock()
					inFlight++
					if inFlight > maxSeen {
						maxSeen = inFlight
					}
					mu.Unlock()
					time.Sleep(time.Millisecond)
					mu.Lock()
					inFlight--
					mu.Unlock()
				}()
			}
			wg.Wait()
			if maxSeen > limit {
				t.Fatalf("expected at most %d concurrent callbacks, observed %d", limit, maxSeen)
			}
		})
	}

	// A non-positive limit means unbounded: Acquire/Release must be no-ops and
	// never block.
	unbounded := newWalkSemaphore(0)
	unbounded.Acquire()
	unbounded.Release()
}
