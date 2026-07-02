// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"sync/atomic"
	"testing"
	"time"

	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/zclconf/go-cty/cty"
)

// emitLock contention benchmark (hc-terraform-wdc.5).
//
// Goal: decide whether emitLock is actually the throughput ceiling under
// parallel fan-out before restructuring emission. emitLock is held in every
// Emit* across (1) a snapshot clone, (2) the ui.* render, and (3) a loop over
// arbitrary hook.* callbacks. These benchmarks measure the parallel emission
// path with a hook that simulates realistic per-callback work, and isolate how
// much of the critical section is the clone (a cheap-win candidate to move out
// of the lock).
//
// Run:
//   go test -run x -bench 'BenchmarkEmit' -cpu 1,4,8 ./internal/runbooks/graph/
//   go test -run x -bench 'BenchmarkEmitExecutedStepParallel' \
//       -mutexprofile mu.out ./internal/runbooks/graph/ && go tool pprof -top mu.out

// benchHook simulates an observer whose callbacks cost a fixed amount of wall
// time (UI render + downstream notification). workPerCall is the per-callback
// cost; counter records how many callbacks landed.
type benchHook struct {
	workPerCall time.Duration
	counter     int64
}

func (h *benchHook) spin() (HookAction, error) {
	atomic.AddInt64(&h.counter, 1)
	if h.workPerCall > 0 {
		// Busy-ish wait: a real hook does CPU+IO, not a pure sleep. Use a short
		// sleep to model latency without pinning a core in CI.
		time.Sleep(h.workPerCall)
	}
	return HookActionContinue, nil
}

func (h *benchHook) PlannedStep(*runbookruntime.Step) (HookAction, error) { return h.spin() }
func (h *benchHook) PlannedStepInfo(StepPlanInfo) (HookAction, error)     { return h.spin() }
func (h *benchHook) ExecutingStep(*runbookruntime.Step) (HookAction, error) {
	return h.spin()
}
func (h *benchHook) ExecutedStep(*runbookruntime.Step) (HookAction, error) { return h.spin() }
func (h *benchHook) ActionEvent(ActionExecEvent) (HookAction, error)       { return h.spin() }
func (h *benchHook) CatchTriggered(string, string) (HookAction, error)     { return h.spin() }
func (h *benchHook) CatchCompleted(string, string) (HookAction, error)     { return h.spin() }
func (h *benchHook) CatchSkipped(string, string) (HookAction, error)       { return h.spin() }
func (h *benchHook) CatchFailed(string, string) (HookAction, error)        { return h.spin() }

func benchStep() *runbookruntime.Step {
	return &runbookruntime.Step{
		Name:    "alpha",
		Index:   0,
		Status:  runbookruntime.StepStatusCompleted,
		Outputs: cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("abc123")}),
	}
}

func benchEvalContext(workPerCall time.Duration) *BuiltinEvalContext {
	return NewEvalContext(EvalContextOpts{
		Hooks: []Hook{&benchHook{workPerCall: workPerCall}},
	})
}

// BenchmarkEmitExecutedStepParallel measures the step-emission path (which
// clones under the lock) from many goroutines at once. Compare -cpu 1 vs 8: if
// throughput does not improve with more cores, emission is serialized on
// emitLock. workPerCall=0 isolates the lock+clone overhead itself.
func BenchmarkEmitExecutedStepParallel(b *testing.B) {
	for _, work := range []time.Duration{0, 10 * time.Microsecond, 100 * time.Microsecond} {
		b.Run(durLabel(work), func(b *testing.B) {
			ec := benchEvalContext(work)
			step := benchStep()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_, _ = ec.EmitExecutedStep(step)
				}
			})
		})
	}
}

// BenchmarkEmitActionEventParallel measures the fire-and-forget notification
// path (no clone) under parallel fan-out — the strongest candidate to move off
// the step critical path, since its HookAction return is already ignored by
// callers.
func BenchmarkEmitActionEventParallel(b *testing.B) {
	for _, work := range []time.Duration{0, 10 * time.Microsecond, 100 * time.Microsecond} {
		b.Run(durLabel(work), func(b *testing.B) {
			ec := benchEvalContext(work)
			event := ActionExecEvent{StepName: "alpha", ActionType: "test_action", Status: "completed"}
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_, _ = ec.EmitActionEvent(event)
				}
			})
		})
	}
}

// BenchmarkCloneRuntimeStepValue measures the snapshot clone in isolation. If
// this is a meaningful fraction of the EmitExecutedStep critical section, moving
// the clone outside emitLock is a cheap, semantics-preserving win.
func BenchmarkCloneRuntimeStepValue(b *testing.B) {
	step := benchStep()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cloneRuntimeStepValue(step)
	}
}

func durLabel(d time.Duration) string {
	if d == 0 {
		return "work-0"
	}
	return "work-" + d.String()
}
