# Runbook Native Execution Implementation Guide

## Purpose

This document describes how to implement Terraform runbooks from scratch on top of `main`, aiming directly at the desired end state.

The target architecture is:

```text
runbook source
-> parse + validate
-> dependency analysis
-> deterministic step expansion
-> compile operation intents into a saved runbook plan
-> later execute that saved plan directly
```

The target architecture is not:

```text
runbook source
-> generate synthetic Terraform files
-> save generated files in a plan
-> later re-run Terraform Core over the generated files
```

This plan is written as though runbooks do not already exist. If code already exists in your working branch, use this document as the intended architecture, not as a description of current implementation.

## What Success Looks Like

At the end of this work, Terraform should support:

1. loading runbook configuration from source files
2. analyzing step dependencies
3. planning a runbook into a durable saved plan artifact
4. freezing all step instances at plan time
5. executing that saved plan later without re-planning topology
6. reevaluating config and outputs against runtime values where appropriate
7. rendering useful human-readable plan and execute output

## Non-Negotiable Semantics

These are the design rules to implement first and keep throughout the project.

1. `runbook plan` freezes topology.
2. `runbook execute` consumes the saved plan and must not change the approved graph.
3. `count` and `for_each` must be fully known during planning.
4. Runtime-only values may affect execute-time config and outputs, but not instance expansion.
5. The saved plan must contain the source bundle used to create it.
6. The saved plan must contain the variable values used during planning.
7. Any plan-time discovery results used to expand later steps must be persisted in the saved plan.
8. Execution must use the bundled plan sources, not the mutable working directory.

## Build Order

Implement the system in the order below. Do not start with CLI polish. Do not start with transport. Start with the core semantics and a single end-to-end test target.

## Step 1: Add An Early End-To-End Test Skeleton

Before writing most of the implementation, create one e2e test that defines the desired behavior and gives you a progress marker.

The first e2e test should prove this narrow scenario:

1. a runbook with two steps
2. the first step performs plan-time discovery
3. the second step uses `for_each` from the first step's planned result
4. `runbook plan` saves the expanded instances
5. `runbook execute` later executes exactly those saved instances

The first version of the test can be skipped or partially asserted while the implementation is incomplete, but it should exist early.

Suggested test shape:

```go
func TestRunbookPlanAndExecuteFreezesForEachExpansion(t *testing.T) {
	// Arrange:
	// - synthetic provider / fake runtime with predictable discovery results
	// - runbook source bundle with:
	//   step "discover"
	//   step "act" { for_each = step.discover.targets }

	// Act:
	// - run plan
	// - inspect saved plan for expanded instances
	// - run execute from saved plan

	// Assert:
	// - planned step instances are frozen in the saved plan
	// - execute reuses the saved instance list
	// - outputs are derived from saved topology, not recomputed topology
}
```

This test is not meant to be the complete test suite. It is the first project checkpoint.

### Agent Prompt For Early E2E Test

Use this prompt with an agent as soon as the first planner/executor interfaces exist:

```text
Create one focused end-to-end test for native runbook planning/execution.

Scenario:
- a discovery step produces a collection during plan
- a downstream step uses for_each over that collection
- the saved plan freezes the expanded step instances
- execute later reuses the saved topology rather than recomputing it

Keep the test as small as possible. Prefer fake or stub provider/runtime behavior over broad fixture setup. If implementation is incomplete, add the narrowest skipped assertions needed so the test can evolve with the feature.
```

## Step 2: Define The Core Data Model

Create the package that represents runbook configuration in memory.

Suggested package:

- `internal/runbooks/runbookconfig`

Suggested types:

```go
type Config struct {
	Files   map[string]*File
	Steps   map[string]*Step
	Outputs map[string]*Output
}

type Step struct {
	Name           string
	Count          hcl.Expression
	ForEach        hcl.Expression
	Actions        []*Action
	DataSources    []*DataSource
	Lists          []*List
	Outputs        map[string]*Output
	Preconditions  []*Condition
	Postconditions []*Condition
	DeclRange      tfdiags.SourceRange
}

type Action struct {
	Type      string
	Name      string
	Config    hcl.Body
	DeclRange tfdiags.SourceRange
}

type DataSource struct {
	Type      string
	Name      string
	Config    hcl.Body
	DeclRange tfdiags.SourceRange
}

type List struct {
	Provider  string
	Type      string
	Name      string
	Config    hcl.Body
	DeclRange tfdiags.SourceRange
}

type Output struct {
	Name      string
	Value     hcl.Expression
	DeclRange tfdiags.SourceRange
}
```

Implementation notes:

1. preserve raw HCL bodies and expressions
2. preserve source ranges for diagnostics and later source references
3. do not design around generated Terraform config

## Step 3: Implement Source Loading And Decoding

Add the ability to load runbook configuration from a directory and from an in-memory source bundle.

Suggested entrypoints:

```go
func LoadConfigDir(path string) (*Config, tfdiags.Diagnostics)
func LoadConfigSources(configPath string, sources map[string][]byte) (*Config, tfdiags.Diagnostics)
```

Requirements:

1. load all runbook-relevant files in stable order
2. decode step blocks, runbook-level outputs, and any supporting declarations
3. preserve original filenames and ranges
4. return diagnostics without losing partial config where practical

## Step 4: Implement Reference Evaluation Scope

Create the scope model used for planning and execution.

Suggested type:

```go
type EvalScope struct {
	Variables    cty.Value
	Steps        cty.Value
	Actions      cty.Value
	Workspace    cty.Value
	Count        cty.Value
	Each         cty.Value
	ExternalFuncs lang.ExternalFuncs
}
```

Also implement expression evaluation helpers:

```go
func EvalExpr(expr hcl.Expression, scope EvalScope, want cty.Type) (cty.Value, tfdiags.Diagnostics)
func EvaluateStepForPlan(step *Step, scope EvalScope) StepEvaluation
func EvaluateStepForExecution(step *Step, preScope, postScope EvalScope) StepEvaluation
```

Requirements:

1. support references to variables, prior steps, workspace values, and action outputs
2. make preconditions and postconditions first-class
3. keep evaluation logic independent of CLI and transport

## Step 5: Implement Dependency Analysis

Add a dependency analyzer that infers step-to-step edges from references.

Suggested entrypoint:

```go
func AnalyzeDependencies(cfg *Config) (*StepDependencies, tfdiags.Diagnostics)
```

Requirements:

1. detect references from one step to another
2. infer dependencies from outputs and conditions
3. reject dependency cycles
4. return a dependency map suitable for deterministic graph walking

## Step 6: Define The Saved Plan Format

Create the package that represents a compiled saved runbook plan.

Suggested package:

- `internal/runbooks/runbookplanfile`

Suggested types:

```go
type Plan struct {
	Version          int                    `json:"version"`
	ConfigPath       string                 `json:"config_path"`
	Workspace        string                 `json:"workspace"`
	CreatedAt        string                 `json:"created_at"`
	TerraformVersion string                 `json:"terraform_version"`

	Variables        map[string][]byte      `json:"variables,omitempty"`
	Sources          map[string][]byte      `json:"sources,omitempty"`
	WorkspaceState   []byte                 `json:"workspace_state,omitempty"`
	ProviderLocks    []ProviderLock         `json:"provider_locks,omitempty"`

	StepOrder        []string               `json:"step_order"`
	Steps            []StepPlan             `json:"steps"`
}

type StepPlan struct {
	Name             string                 `json:"name"`
	BaseName         string                 `json:"base_name,omitempty"`
	After            []string               `json:"after,omitempty"`

	KnownSkipped     bool                   `json:"known_skipped,omitempty"`
	SkipReason       string                 `json:"skip_reason,omitempty"`

	CountIndex       *int64                 `json:"count_index,omitempty"`
	EachKey          []byte                 `json:"each_key,omitempty"`
	EachValue        []byte                 `json:"each_value,omitempty"`

	DeclaredOutputs  []string               `json:"declared_outputs,omitempty"`
	PlannedOutputs   map[string][]byte      `json:"planned_outputs,omitempty"`

	Operations       []OperationPlan        `json:"operations,omitempty"`
}

type OperationPlan struct {
	Kind             OperationKind          `json:"kind"`
	Phase            OperationPhase         `json:"phase"`
	Address          string                 `json:"address"`
	Provider         string                 `json:"provider,omitempty"`
	Type             string                 `json:"type,omitempty"`
	Name             string                 `json:"name,omitempty"`

	SourceRef        SourceRef              `json:"source_ref"`
	PreviewConfig    map[string][]byte      `json:"preview_config,omitempty"`
	DeferredAttrs    []string               `json:"deferred_attrs,omitempty"`

	PlannedResult    []byte                 `json:"planned_result,omitempty"`
}

type SourceRef struct {
	Filename         string                 `json:"filename"`
	StartLine        int                    `json:"start_line"`
	EndLine          int                    `json:"end_line"`
}
```

Use `ctymsgpack` for all saved dynamic values.

This format is the durable contract for plan and execute.

## Step 7: Define Operation Semantics

Add the operation kinds and phases used by the planner and executor.

```go
type OperationKind string

const (
	OperationKindAction OperationKind = "action"
	OperationKindData   OperationKind = "data"
	OperationKindList   OperationKind = "list"
)

type OperationPhase string

const (
	OperationPhasePlan    OperationPhase = "plan"
	OperationPhaseExecute OperationPhase = "execute"
)
```

Semantics:

1. `plan`-phase operations may run during planning and persist `PlannedResult`
2. `execute`-phase operations only run during execution
3. if an operation result is needed for `count` or `for_each`, it must be `plan` phase

## Step 8: Add Stable Source References

Every planned operation should point back to the original runbook block it came from.

Suggested helper:

```go
func sourceRef(r tfdiags.SourceRange) runbookplanfile.SourceRef {
	return runbookplanfile.SourceRef{
		Filename:  filepath.Base(r.Filename),
		StartLine: r.Start.Line,
		EndLine:   r.End.Line,
	}
}
```

Requirements:

1. execute must be able to locate and reevaluate the original block from `plan.Sources`
2. source references must remain stable after the plan is saved

## Step 9: Add Variable And Instance Binding Encoding

Create helpers to persist the values required to reconstruct exact instance scopes.

Suggested helpers:

```go
func encodeDynamic(v cty.Value) []byte
func decodeDynamic(raw []byte) cty.Value

func encodeCountIndex(v cty.Value) *int64 {
	if v == cty.NilVal || !v.IsKnown() || v.IsNull() {
		return nil
	}
	n := int64(v.AsBigFloat().Int64())
	return &n
}

func encodeEach(scope runbookconfig.EvalScope) ([]byte, []byte) {
	var key, val []byte
	if scope.Each != cty.NilVal && scope.Each.Type().IsObjectType() {
		if scope.Each.Type().HasAttribute("key") {
			key = encodeDynamic(scope.Each.GetAttr("key"))
		}
		if scope.Each.Type().HasAttribute("value") {
			val = encodeDynamic(scope.Each.GetAttr("value"))
		}
	}
	return key, val
}
```

These bindings must be stored on each `StepPlan`.

## Step 10: Implement Schema-Backed Config Preview Evaluation

Create a helper that evaluates operation config bodies into previewable attribute values for the saved plan.

Suggested helper:

```go
func evalConfigPreview(
	body hcl.Body,
	schema *configschema.Block,
	scope runbookconfig.EvalScope,
) (map[string][]byte, []string, tfdiags.Diagnostics)
```

Example shape:

```go
func evalConfigPreview(...) (map[string][]byte, []string, tfdiags.Diagnostics) {
	var deferred []string
	ret := map[string][]byte{}

	lscope := &lang.Scope{
		Data:          &runbookPlanLangData{scope: scope},
		ParseRef:      addrs.ParseRefFromRunbookScope,
		ExternalFuncs: scope.ExternalFuncs,
	}

	expanded, diags := lscope.ExpandBlock(body, schema)
	if diags.HasErrors() {
		return nil, nil, diags
	}

	val, moreDiags := lscope.EvalBlock(expanded, schema)
	diags = diags.Append(moreDiags)
	if val == cty.NilVal || !val.Type().IsObjectType() {
		return ret, deferred, diags
	}

	for name, attr := range val.AsValueMap() {
		if !attr.IsKnown() {
			deferred = append(deferred, name)
		}
		ret[name] = encodeDynamic(attr)
	}

	sort.Strings(deferred)
	return ret, deferred, diags
}
```

This preview is for human-readable planning. It is not the execute-time source of truth.

## Step 11: Define Planner Runtime Interfaces

Create an abstraction for plan-time provider-backed operations.

Suggested package:

- `internal/runbooks/runbookplan`

Suggested interfaces:

```go
type PlanningRuntime interface {
	ProviderSchemas(context.Context) (ProviderSchemaIndex, tfdiags.Diagnostics)
	RunPlannableOperation(context.Context, runbookplanfile.OperationPlan, runbookconfig.EvalScope) (cty.Value, tfdiags.Diagnostics)
	WorkspaceScope(context.Context) (cty.Value, tfdiags.Diagnostics)
	WorkspaceState(context.Context) ([]byte, tfdiags.Diagnostics)
	ProviderLocks(context.Context) ([]runbookplanfile.ProviderLock, tfdiags.Diagnostics)
}
```

This runtime is used only by the planner.

## Step 12: Implement Operation Compilation

Add a compiler that converts `Action`, `DataSource`, and `List` blocks into `OperationPlan` values.

Suggested entrypoint:

```go
func compileOperations(
	step *runbookconfig.Step,
	scope runbookconfig.EvalScope,
	schemas ProviderSchemaIndex,
) ([]runbookplanfile.OperationPlan, tfdiags.Diagnostics)
```

The compiler should:

1. determine the `Kind`
2. determine the `Phase`
3. compute `Address`
4. capture `Type`, `Name`, and `Provider`
5. capture `SourceRef`
6. compute `PreviewConfig`
7. record any `DeferredAttrs`

Example shape:

```go
func compileAction(action *runbookconfig.Action, scope runbookconfig.EvalScope, schema providers.ActionSchema) (runbookplanfile.OperationPlan, tfdiags.Diagnostics) {
	preview, deferred, diags := evalConfigPreview(action.Config, schema.ConfigSchema, scope)
	return runbookplanfile.OperationPlan{
		Kind:          runbookplanfile.OperationKindAction,
		Phase:         runbookplanfile.OperationPhaseExecute,
		Address:       fmt.Sprintf("action.%s.%s", action.Type, action.Name),
		Type:          action.Type,
		Name:          action.Name,
		SourceRef:     sourceRef(action.DeclRange),
		PreviewConfig: preview,
		DeferredAttrs: deferred,
	}, diags
}
```

## Step 13: Implement Expansion Determinism Validation

Create a validation pass that explicitly rejects non-plannable expansion.

Suggested entrypoint:

```go
func ValidateExpansionDeterminism(step *runbookconfig.Step, scope runbookconfig.EvalScope) tfdiags.Diagnostics
```

Rules:

1. `count` must evaluate to a known integer during planning
2. `for_each` must evaluate to a known collection during planning
3. if either expression depends on execute-phase values, return an error

Example diagnostic:

```go
return tfdiags.Sourceless(
	tfdiags.Error,
	"Runbook step expansion is not plannable",
	`The "for_each" expression for step "act" depends on values that are only available during execute.`,
)
```

## Step 14: Implement Plan-Time Discovery

Add the ability for the planner to run operations whose results are needed to determine topology.

Suggested helper:

```go
func runPlannableOperation(
	ctx context.Context,
	op runbookplanfile.OperationPlan,
	scope runbookconfig.EvalScope,
	runtime PlanningRuntime,
) (cty.Value, tfdiags.Diagnostics)
```

Requirements:

1. evaluate the operation config for plan-time scope
2. invoke the provider-backed operation
3. capture the result in `PlannedResult`
4. feed that result into later planning scope values

This is how a discovery step can drive downstream `for_each`.

## Step 15: Build The Planning Graph Walker

Create the planner that walks steps in dependency order, expands instances, compiles operations, and returns a saved plan.

Suggested package:

- `internal/runbooks/runbookplan`

Suggested entrypoint:

```go
func BuildCompiledPlan(
	ctx context.Context,
	cfg *runbookconfig.Config,
	sources map[string][]byte,
	vars cty.Value,
	runtime PlanningRuntime,
) (*runbookplanfile.Plan, tfdiags.Diagnostics)
```

High-level algorithm:

1. analyze dependencies
2. compute dependency order
3. initialize step results store
4. for each step in order:
   - build plan-time base scope
   - validate expansion determinism
   - expand singleton / count / for_each instances
   - for each instance:
     - compile operations
     - run any plan-phase operations
     - compute planned outputs
     - write a `StepPlan`
     - seed known outputs into step results for downstream planning
5. assemble the final `Plan`

Sketch:

```go
func BuildCompiledPlan(...) (*runbookplanfile.Plan, tfdiags.Diagnostics) {
	deps, diags := runbookconfig.AnalyzeDependencies(cfg)
	if diags.HasErrors() {
		return nil, diags
	}

	workspace, moreDiags := runtime.WorkspaceScope(ctx)
	diags = diags.Append(moreDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	results := runbookeval.NewStepResults()
	plan := &runbookplanfile.Plan{Sources: sources}

	for _, stepName := range dependencyOrderedSteps(deps) {
		step := cfg.Steps[stepName]
		baseScope := runbookconfig.EvalScope{
			Variables: vars,
			Steps:     results.ScopeValue(),
			Workspace: workspace,
		}

		// validate expansion, expand instances, compile each instance, append to plan
	}

	return plan, diags
}
```

## Step 16: Add Planner Output Computation

For each compiled step instance, compute the planned output object.

Requirements:

1. include known plan-time outputs
2. preserve unknowns for execute-time values
3. include outputs derived from plan-time discovery operations
4. store `DeclaredOutputs` separately from `PlannedOutputs`

Suggested helper:

```go
func plannedOutputsForStep(step *runbookconfig.Step, scope runbookconfig.EvalScope, ops []runbookplanfile.OperationPlan) (cty.Value, tfdiags.Diagnostics)
```

## Step 17: Implement Plan Persistence

Add read/write support for the saved runbook plan file.

Suggested entrypoints:

```go
func Create(filename string, args CreateArgs) error
func Open(filename string) (*Reader, error)
func (r *Reader) ReadPlan() (*Plan, error)
```

Requirements:

1. persist all `Plan` fields
2. persist bundled sources
3. persist variables and planned results
4. persist workspace snapshot bytes
5. keep format versioned

## Step 18: Define Execute Runtime Interfaces

Create the runtime abstraction used by native execution.

Suggested package:

- `internal/runbooks/runbookexec`

Suggested interfaces:

```go
type Runtime struct {
	Providers ProviderRegistry
	ActionOut ActionStreamer
}

func (r *Runtime) WorkspaceScope(ctx context.Context, plan *runbookplanfile.Plan) (cty.Value, tfdiags.Diagnostics)
func (r *Runtime) ExecuteOperation(ctx context.Context, op runbookplanfile.OperationPlan, scope runbookconfig.EvalScope) (cty.Value, tfdiags.Diagnostics)
func (r *Runtime) ValidatePlanCompatibility(ctx context.Context, plan *runbookplanfile.Plan) tfdiags.Diagnostics
```

Requirements:

1. verify provider/runtime compatibility with the saved plan
2. stream action output if needed
3. invoke operations directly from compiled semantics

## Step 19: Implement Runtime Result Stores

Create the result storage used while executing a saved plan.

```go
type RuntimeResults struct {
	Steps   *runbookeval.StepResults
	Actions map[string]cty.Value
	Data    map[string]cty.Value
	Lists   map[string]cty.Value
}
```

Add helpers to rebuild `EvalScope`:

```go
func (r *RuntimeResults) Scope(stepPlan runbookplanfile.StepPlan, vars cty.Value, workspace cty.Value) runbookconfig.EvalScope {
	return runbookconfig.EvalScope{
		Variables: vars,
		Steps:     r.Steps.ScopeValue(),
		Actions:   actionScopeValue(r.Actions),
		Workspace: workspace,
		Count:     countScopeValue(stepPlan.CountIndex),
		Each:      eachScopeValue(stepPlan.EachKey, stepPlan.EachValue),
	}
}
```

## Step 20: Reevaluate Original Source At Execute Time

Execute-time config must come from the original bundled source block, not the preview values saved in the plan.

Add a helper like:

```go
func evaluateOperationConfig(
	sources map[string][]byte,
	ref runbookplanfile.SourceRef,
	schema *configschema.Block,
	scope runbookconfig.EvalScope,
) (cty.Value, tfdiags.Diagnostics)
```

This helper should:

1. locate the source file in `plan.Sources`
2. reparse the source
3. extract the referenced block
4. evaluate its config body against current execute-time scope
5. return a typed object value

## Step 21: Implement Native Operation Execution

Implement direct execution for each `OperationKind`.

Suggested dispatch:

```go
func (r *Runtime) ExecuteOperation(
	ctx context.Context,
	op runbookplanfile.OperationPlan,
	scope runbookconfig.EvalScope,
) (cty.Value, tfdiags.Diagnostics) {
	switch op.Kind {
	case runbookplanfile.OperationKindAction:
		return r.invokeAction(ctx, op, scope)
	case runbookplanfile.OperationKindData:
		return r.readData(ctx, op, scope)
	case runbookplanfile.OperationKindList:
		return r.runList(ctx, op, scope)
	default:
		return cty.NilVal, tfdiags.Diagnostics{tfdiags.Sourceless(tfdiags.Error, "Unsupported runbook operation", string(op.Kind))}
	}
}
```

Requirements:

1. do not generate Terraform files
2. do not re-run Terraform Core over synthetic config
3. reevaluate original config against current runtime scope
4. return results as `cty.Value`

## Step 22: Implement Saved Plan Execution

Create the top-level executor.

Suggested entrypoint:

```go
func Execute(
	ctx context.Context,
	plan *runbookplanfile.Plan,
	runtime *Runtime,
) (*ExecutionResult, tfdiags.Diagnostics)
```

High-level algorithm:

1. validate plan/runtime compatibility
2. load config from `plan.Sources`
3. decode variables from `plan.Variables`
4. initialize runtime results stores
5. for each saved `StepPlan` in `StepOrder`:
   - reconstruct exact instance scope from saved bindings
   - if step is marked skipped, skip it
   - replay any `plan`-phase `PlannedResult`
   - execute any `execute`-phase operations
   - compute final outputs
   - evaluate postconditions
   - write outputs into step results store
6. return the final execution result

Sketch:

```go
func Execute(...) (*ExecutionResult, tfdiags.Diagnostics) {
	if diags := runtime.ValidatePlanCompatibility(ctx, plan); diags.HasErrors() {
		return nil, diags
	}

	cfg, diags := runbookconfig.LoadConfigSources(plan.ConfigPath, plan.Sources)
	if diags.HasErrors() {
		return nil, diags
	}

	vars := decodeVariables(plan.Variables)
	workspace, moreDiags := runtime.WorkspaceScope(ctx, plan)
	diags = diags.Append(moreDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	results := NewRuntimeResults()
	for _, stepName := range plan.StepOrder {
		stepPlan := lookupStepPlan(plan, stepName)
		stepCfg := cfg.Steps[stepPlan.BaseName]
		scope := results.Scope(stepPlan, vars, workspace)

		stepResult, moreDiags := executeStep(ctx, plan, stepCfg, stepPlan, scope, runtime)
		diags = diags.Append(moreDiags)
		if moreDiags.HasErrors() {
			return nil, diags
		}

		results.Merge(stepResult)
	}

	return &ExecutionResult{Results: results}, diags
}
```

## Step 23: Evaluate Final Outputs Using Existing Expression Evaluation

Do not create synthetic output blocks. Use the runbook expression evaluator directly.

Suggested helper:

```go
func evaluateFinalStepOutputs(step *runbookconfig.Step, scope runbookconfig.EvalScope) (cty.Value, tfdiags.Diagnostics) {
	vals := map[string]cty.Value{}
	for name, output := range step.Outputs {
		if output == nil || output.Value == nil {
			continue
		}
		val, diags := runbookconfig.EvalExpr(output.Value, scope, cty.DynamicPseudoType)
		if diags.HasErrors() {
			return cty.NilVal, diags
		}
		vals[name] = val
	}
	if len(vals) == 0 {
		return cty.EmptyObjectVal, nil
	}
	return cty.ObjectVal(vals), nil
}
```

Then preserve the semantic flow:

```go
preEval := runbookconfig.EvaluateStepForPlan(step, preScope)
postEval := runbookconfig.EvaluateStepForExecution(step, preScope, postScope)
```

## Step 24: Add CLI Integration Last

Once planner, plan persistence, and executor exist, add CLI orchestration.

Suggested responsibilities for `internal/command/runbook.go`:

1. load variable values
2. build planning runtime
3. call `BuildCompiledPlan`
4. persist the plan file
5. load a saved plan file
6. call `runbookexec.Execute`
7. render human-readable plan and execute output

The CLI should be thin. It should not own core semantics.

## Step 25: Add RPC Integration After CLI Is Stable

Only after the core local planner/executor work, wire them through `internal/rpcapi/runbooks.go`.

Transport should carry compiled runbook semantics.

Suggested direction:

```go
type PlanRunbook_Response struct {
	Plan         *CompiledPlan
	Diagnostics  []*Diagnostic
}

type ExecuteRunbook_Request struct {
	Plan         *CompiledPlan
}
```

The important part is that transport carries compiled plan data, not generated Terraform files.

## Step 26: Render Human Plan Output From The Compiled Plan

Use `OperationPlan` and `PlannedOutputs` to render UX.

Basic shape:

```go
for _, step := range plan.Steps {
	fmt.Printf("# %s\n", step.Name)
	for _, op := range step.Operations {
		fmt.Printf("  %s %s.%s\n", op.Kind, op.Type, op.Name)
	}
}
```

Richer shape:

```go
action "aws_ssm_send_command" "restart" {
  instance_id = "i-123"
  document    = "AWS-RunShellScript"
  parameters  = (known after execute)
}
```

This should come from `PreviewConfig` and `DeferredAttrs`, not from rendered synthetic HCL.

## Step 27: Persist And Reuse Plan-Time Results During Execute

If an operation ran during planning, execute must replay the saved result rather than rerun it.

Example:

```go
if op.Phase == runbookplanfile.OperationPhasePlan {
	val := decodeDynamic(op.PlannedResult)
	result.Record(op, val)
	continue
}
```

This is what preserves the saved-plan contract.

## Step 28: Build The Full Test Suite After The Core Exists

Once the early e2e test is in place and the core planner/executor exists, expand coverage in this order:

1. `runbookconfig`
   - decoding
   - expression evaluation
   - conditions

2. `runbookplan`
   - dependency ordering
   - expansion determinism
   - singleton / count / for_each expansion
   - plan-time discovery driving downstream expansion

3. `runbookplanfile`
   - round-trip persistence of variables, sources, planned results, and instance bindings

4. `runbookexec`
   - execute action/data/list operations
   - replay saved plan-time results
   - compute final outputs
   - evaluate postconditions

5. CLI / E2E
   - saved plan executes from bundled sources
   - changing working tree files after planning does not affect execution
   - plan-time topology remains stable during execute

### Agent Prompt For Full Test Pass

Use this prompt with an agent once the implementation is mostly complete:

```text
Write and run focused tests for the native runbook planner and executor.

Start by strengthening the existing end-to-end test that freezes for_each expansion from a plan-time discovery step.

Then add:
- unit tests for config loading, expression evaluation, and conditions
- planner tests for dependency ordering and deterministic expansion
- planner tests for plan-time discovery feeding downstream expansion
- planner tests rejecting execute-time-only expansion
- planfile persistence round-trip tests for variables, sources, planned results, and instance bindings
- executor tests for replaying plan-phase results, executing execute-phase operations, and computing final outputs
- integration tests proving execute uses saved plan sources rather than the mutable working tree

Prefer small tests with fakes over broad fixtures. Run only the relevant Go tests you add or touch.
```

## Step 29: Keep These Principles Visible During Implementation

1. preserve raw source and source ranges
2. freeze topology at plan time
3. bundle the source of truth into the saved plan
4. reevaluate original config at execute time where values are intentionally deferred
5. never make generated Terraform files part of the durable contract
6. keep planner and executor as the semantic center
7. add CLI and RPC only after the core works locally

## Minimal Architectural Sketch

This is the intended final shape in one place:

```go
func PlanRunbook(ctx context.Context, configPath string, vars cty.Value, runtime runbookplan.PlanningRuntime) (*runbookplanfile.Plan, tfdiags.Diagnostics) {
	cfg, diags := runbookconfig.LoadConfigDir(configPath)
	if diags.HasErrors() {
		return nil, diags
	}

	sources := readSources(configPath)
	return runbookplan.BuildCompiledPlan(ctx, cfg, sources, vars, runtime)
}

func ExecuteRunbook(ctx context.Context, plan *runbookplanfile.Plan, runtime *runbookexec.Runtime) (*runbookexec.ExecutionResult, tfdiags.Diagnostics) {
	return runbookexec.Execute(ctx, plan, runtime)
}
```

## Final Checklist

1. core runbook config model exists
2. runbook source loading exists
3. evaluation scope and condition evaluation exist
4. dependency analysis exists
5. deterministic expansion validation exists
6. compiled saved plan format exists
7. plan-time discovery exists
8. planner freezes all step instances
9. saved plan bundles sources and variables
10. executor uses the saved plan rather than the working tree
11. executor reevaluates original config blocks from bundled sources
12. native operation execution replaces synthetic Terraform execution
13. early end-to-end test exists and is strengthened over time
14. full test pass is delegated to an agent using the prompts above
