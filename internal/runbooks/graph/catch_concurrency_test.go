// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform/internal/configs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// rendezvousCatchHook blocks in CatchTriggered until exactly `parties` distinct
// catch executions have arrived, then releases them all at once. This forces
// two concurrent catch executions to be simultaneously in-flight between their
// precondition evaluation and their output evaluation — precisely the window in
// which the old single global failed_step slot would have been stomped by the
// sibling failure.
type rendezvousCatchHook struct {
	entered  sync.WaitGroup
	released chan struct{}
	timedOut chan struct{}
	once     sync.Once
}

func newRendezvousCatchHook(parties int) *rendezvousCatchHook {
	h := &rendezvousCatchHook{
		released: make(chan struct{}),
		timedOut: make(chan struct{}),
	}
	h.entered.Add(parties)
	go func() {
		h.entered.Wait()
		close(h.released)
	}()
	return h
}

func (h *rendezvousCatchHook) CatchTriggered(string, string) (HookAction, error) {
	h.entered.Done()
	select {
	case <-h.released:
	case <-time.After(5 * time.Second):
		// A sibling never arrived: the catches were serialized (or one was
		// wrongly skipped). Signal so the test fails deterministically rather
		// than hanging.
		h.once.Do(func() { close(h.timedOut) })
	}
	return HookActionContinue, nil
}

func (h *rendezvousCatchHook) PlannedStep(*runbookruntime.Step) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *rendezvousCatchHook) PlannedStepInfo(StepPlanInfo) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *rendezvousCatchHook) ExecutingStep(*runbookruntime.Step) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *rendezvousCatchHook) ExecutedStep(*runbookruntime.Step) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *rendezvousCatchHook) ActionEvent(ActionExecEvent) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *rendezvousCatchHook) CatchCompleted(string, string) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *rendezvousCatchHook) CatchSkipped(string, string) (HookAction, error) {
	return HookActionContinue, nil
}
func (h *rendezvousCatchHook) CatchFailed(string, string) (HookAction, error) {
	return HookActionContinue, nil
}

// TestConcurrentCatchBlocksSeeOwnFailedStep is the regression test for
// hc-terraform-wdc.4. Two independent steps fail concurrently, each handled by
// its own catch block (selected by a precondition on failed_step.name) that
// records failed_step.name as an output. A rendezvous barrier guarantees both
// catch executions are in-flight at the same time before either evaluates its
// output. Each catch must observe ITS OWN failed step, not the sibling's.
//
// Under the previous design — a single shared ec.failedStep slot set at the top
// of executeCatchBlocks — the second failure would overwrite the first before
// the outputs were evaluated, so at least one catch would read the wrong
// failed_step (or be skipped by a stomped precondition, deadlocking the
// barrier). Either way this test would fail against the old behavior. With the
// failed step threaded per-evaluation it passes deterministically.
func TestConcurrentCatchBlocksSeeOwnFailedStep(t *testing.T) {
	config := &runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"alpha": {Name: "alpha"},
			"beta":  {Name: "beta"},
		},
		Catches: map[string]*runbookconfigs.Catch{
			"ca": {
				Name: "ca",
				Preconditions: []*runbookconfigs.Condition{{
					Kind:      runbookconfigs.PreconditionCondition,
					Condition: mustParseExpression(t, `failed_step.name == "alpha"`),
				}},
				Outputs: []*configs.Output{{Name: "observed", Expr: mustParseExpression(t, `failed_step.name`)}},
			},
			"cb": {
				Name: "cb",
				Preconditions: []*runbookconfigs.Condition{{
					Kind:      runbookconfigs.PreconditionCondition,
					Condition: mustParseExpression(t, `failed_step.name == "beta"`),
				}},
				Outputs: []*configs.Output{{Name: "observed", Expr: mustParseExpression(t, `failed_step.name`)}},
			},
		},
	}

	hook := newRendezvousCatchHook(2)
	// A single shared eval context, exactly as the parallel graph walk uses.
	ctx := NewEvalContext(EvalContextOpts{Config: config, Hooks: []Hook{hook}})

	stepAlpha := &runbookruntime.Step{Name: "alpha", Index: 0}
	stepBeta := &runbookruntime.Step{Name: "beta", Index: 1}
	diagsAlpha := tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(tfdiags.Error, "alpha failed", "boom-a"))
	diagsBeta := tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(tfdiags.Error, "beta failed", "boom-b"))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); executeCatchBlocks(ctx, stepAlpha, diagsAlpha) }()
	go func() { defer wg.Done(); executeCatchBlocks(ctx, stepBeta, diagsBeta) }()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("concurrent catch execution did not complete: the catch blocks were serialized")
	}

	select {
	case <-hook.timedOut:
		t.Fatal("a catch block timed out at the rendezvous: the two failures were not handled concurrently")
	default:
	}

	caObserved, ok := ctx.CatchOutput("ca", "observed")
	if !ok {
		t.Fatal("catch 'ca' (for step alpha) did not run; its precondition saw the wrong failed_step")
	}
	if got := caObserved.AsString(); got != "alpha" {
		t.Fatalf("catch 'ca' observed failed_step.name = %q, want \"alpha\" (failed_step was stomped by the concurrent beta failure)", got)
	}

	cbObserved, ok := ctx.CatchOutput("cb", "observed")
	if !ok {
		t.Fatal("catch 'cb' (for step beta) did not run; its precondition saw the wrong failed_step")
	}
	if got := cbObserved.AsString(); got != "beta" {
		t.Fatalf("catch 'cb' observed failed_step.name = %q, want \"beta\" (failed_step was stomped by the concurrent alpha failure)", got)
	}
}
