// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"reflect"
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/runbooks/runbookgraph"
	"github.com/spf13/afero"
	"github.com/zclconf/go-cty/cty"
)

func TestNewContext(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", `
output "root_value" { value = "hello" }

action "http" "notify" {}
`)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "name" {
  type = string
}

output "summary" {
  value = step.deploy.result
}

step "deploy" {
  locals {
    greeting = "hello"
  }

  data "aws_instance" "example" {}

  list "aws_instance" "example" {
    provider = aws
  }

  action "http" "notify" {}

  output "result" {
    value = "ok"
  }

  execute {
    invoke_action {
      action = workspace.action.http.notify
    }
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if ctx == nil {
		t.Fatal("expected context but got nil")
	}
	if ctx.Config() != config {
		t.Fatal("context did not retain config")
	}
	if got := ctx.Variable("name"); got == nil {
		t.Fatal("expected indexed runbook variable")
	}
	if got := ctx.Output("summary"); got == nil {
		t.Fatal("expected indexed runbook output")
	}
	if got := ctx.Step("deploy"); got == nil {
		t.Fatal("expected indexed step")
	}
	if got := ctx.StepLocal("deploy", "greeting"); got == nil {
		t.Fatal("expected indexed step local")
	}
	if got := ctx.StepAction("deploy", "http", "notify"); got == nil {
		t.Fatal("expected indexed step action")
	}
	if got := ctx.StepDataSource("deploy", "aws_instance", "example"); got == nil {
		t.Fatal("expected indexed step data source")
	}
	if got := ctx.StepList("deploy", "aws_instance", "example"); got == nil {
		t.Fatal("expected indexed step list")
	}
	if got := ctx.StepOutput("deploy", "result"); got == nil {
		t.Fatal("expected indexed step output")
	}
	wantActions := []runbookaddrs.ExecutableAction{
		runbookaddrs.WorkspaceActionInstance{Action: addrs.AbsAction{
			Module: addrs.RootModuleInstance,
			Action: addrs.Action{Type: "http", Name: "notify"},
		}, Key: addrs.NoKey},
	}
	if !reflect.DeepEqual(ctx.UsedWorkspaceActions(), wantActions) {
		t.Fatalf("wrong workspace actions\ngot:  %#v\nwant: %#v", ctx.UsedWorkspaceActions(), wantActions)
	}
	if len(ctx.UsedWorkspaceOutputs()) != 0 {
		t.Fatalf("expected no tracked workspace outputs, got %d", len(ctx.UsedWorkspaceOutputs()))
	}
	if diags := ctx.Validate(); diags.HasErrors() {
		t.Fatalf("unexpected validate diagnostics: %s", diags.Err())
	}
	if len(ctx.UsedWorkspaceOutputs()) != 0 {
		t.Fatalf("expected no tracked workspace outputs after validate, got %d", len(ctx.UsedWorkspaceOutputs()))
	}
}

func TestNewContextNilConfig(t *testing.T) {
	ctx, diags := NewContext(&RunbookContextOpts{})
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
	if ctx != nil {
		t.Fatal("expected nil context")
	}
}

func TestRunbookContextStepInstancesSingleton(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  output "result" {
    value = "ok"
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}

	step := ctx.Step("deploy")
	insts, known := step.Instances()
	if !known {
		t.Fatal("expected singleton step instances to be known")
	}
	if len(insts) != 1 {
		t.Fatalf("expected one singleton step instance, got %#v", insts)
	}
	inst := insts[addrs.NoKey]
	if inst == nil {
		t.Fatalf("expected singleton step instance at no key, got %#v", insts)
	}
	if inst.Step() != step {
		t.Fatalf("wrong singleton step instance owner\ngot:  %#v\nwant: %#v", inst.Step(), step)
	}
	if inst.Addr() != (runbookaddrs.StepInstance{Step: runbookaddrs.ConfigStep{Name: "deploy"}}) {
		t.Fatalf("wrong singleton step instance address\ngot:  %#v", inst.Addr())
	}
}

func TestRunbookContextStepInstancesStaticCount(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  count = 2

  output "result" {
    value = count.index
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}

	step := ctx.Step("deploy")
	insts, known := step.Instances()
	if !known {
		t.Fatal("expected counted step instances to be known")
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 step instances, got %#v", insts)
	}
	if insts[addrs.IntKey(0)] == nil || insts[addrs.IntKey(1)] == nil {
		t.Fatalf("wrong counted step keys: %#v", insts)
	}
	if !insts[addrs.IntKey(0)].RepetitionData().CountIndex.RawEquals(cty.NumberIntVal(0)) || !insts[addrs.IntKey(1)].RepetitionData().CountIndex.RawEquals(cty.NumberIntVal(1)) {
		t.Fatalf("wrong count repetition data: %#v", insts)
	}
}

func TestRunbookContextStepInstancesStaticForEach(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  for_each = {
    blue  = "blue"
    green = "green"
  }

  output "result" {
    value = each.key
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}

	step := ctx.Step("deploy")
	insts, known := step.Instances()
	if !known {
		t.Fatal("expected for_each step instances to be known")
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 step instances, got %#v", insts)
	}
	if insts[addrs.StringKey("blue")] == nil || insts[addrs.StringKey("green")] == nil {
		t.Fatalf("wrong for_each step keys: %#v", insts)
	}
	if !insts[addrs.StringKey("blue")].RepetitionData().EachKey.RawEquals(cty.StringVal("blue")) || !insts[addrs.StringKey("blue")].RepetitionData().EachValue.RawEquals(cty.StringVal("blue")) {
		t.Fatalf("wrong first for_each repetition data: %#v", insts[addrs.StringKey("blue")].RepetitionData())
	}
	if !insts[addrs.StringKey("green")].RepetitionData().EachKey.RawEquals(cty.StringVal("green")) || !insts[addrs.StringKey("green")].RepetitionData().EachValue.RawEquals(cty.StringVal("green")) {
		t.Fatalf("wrong second for_each repetition data: %#v", insts[addrs.StringKey("green")].RepetitionData())
	}
}

func TestRunbookContextStepInstancesUnknownDynamicCount(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "instance_count" {
  type = number
}

step "deploy" {
  count = var.instance_count

  output "result" {
    value = "ok"
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}

	insts, known := ctx.Step("deploy").Instances()
	if known {
		t.Fatalf("expected dynamic count step instances to be unknown, got %#v", insts)
	}
}

func TestRunbookContextStepInstancesVariableDefaultCount(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "instance_count" {
  type    = number
  default = 3
}

step "deploy" {
  count = var.instance_count

  output "result" {
    value = count.index
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}

	insts, known := ctx.Step("deploy").Instances()
	if !known {
		t.Fatal("expected variable-default count step instances to be known")
	}
	if len(insts) != 3 {
		t.Fatalf("expected 3 step instances, got %#v", insts)
	}
}

func TestRunbookContextStepInstancesLocalDerivedForEach(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "envs" {
  type    = list(string)
  default = ["blue", "green"]
}

step "deploy" {
  locals {
    targets = toset(var.envs)
  }

  for_each = local.targets

  output "result" {
    value = each.key
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}

	insts, known := ctx.Step("deploy").Instances()
	if !known {
		t.Fatal("expected local-derived for_each step instances to be known")
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 step instances, got %#v", insts)
	}
	if insts[addrs.StringKey("blue")] == nil || insts[addrs.StringKey("green")] == nil {
		t.Fatalf("wrong local-derived for_each keys: %#v", insts)
	}
}

func TestRunbookContextUnknownCountStepInstance(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "instance_count" {
  type = number
}

step "deploy" {
  count = var.instance_count

  output "result" {
    value = count.index
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}

	step := ctx.Step("deploy")
	inst := step.UnknownInstance(addrs.IntKey(2))
	if inst == nil {
		t.Fatal("expected unknown count step instance")
	}
	if inst.Step() != step {
		t.Fatalf("wrong unknown count step owner\ngot: %#v\nwant: %#v", inst.Step(), step)
	}
	if inst.Addr() != (runbookaddrs.StepInstance{Step: runbookaddrs.ConfigStep{Name: "deploy"}, Key: addrs.IntKey(2)}) {
		t.Fatalf("wrong unknown count step addr: %#v", inst.Addr())
	}
	if !inst.RepetitionData().CountIndex.RawEquals(cty.UnknownVal(cty.Number)) {
		t.Fatalf("wrong unknown count repetition data: %#v", inst.RepetitionData())
	}
	if again := step.UnknownInstance(addrs.IntKey(2)); again != inst {
		t.Fatalf("expected unknown count step instance to be memoized\nfirst:  %#v\nsecond: %#v", inst, again)
	}
}

func TestRunbookContextUnknownForEachStepInstance(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "envs" {
  type = set(string)
}

step "deploy" {
  for_each = var.envs

  output "result" {
    value = each.key
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}

	step := ctx.Step("deploy")
	inst := step.UnknownInstance(addrs.StringKey("blue"))
	if inst == nil {
		t.Fatal("expected unknown for_each step instance")
	}
	if inst.Addr() != (runbookaddrs.StepInstance{Step: runbookaddrs.ConfigStep{Name: "deploy"}, Key: addrs.StringKey("blue")}) {
		t.Fatalf("wrong unknown for_each step addr: %#v", inst.Addr())
	}
	if !inst.RepetitionData().EachKey.RawEquals(cty.StringVal("blue")) {
		t.Fatalf("wrong unknown for_each key repetition data: %#v", inst.RepetitionData())
	}
	if !inst.RepetitionData().EachValue.RawEquals(cty.UnknownVal(cty.String)) {
		t.Fatalf("wrong unknown for_each value repetition data: %#v", inst.RepetitionData())
	}
	if again := step.UnknownInstance(addrs.StringKey("blue")); again != inst {
		t.Fatalf("expected unknown for_each step instance to be memoized\nfirst:  %#v\nsecond: %#v", inst, again)
	}
}

func TestRunbookContextUnknownForEachWildcardStepInstance(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "envs" {
  type = set(string)
}

step "deploy" {
  for_each = var.envs

  output "result" {
    value = each.key
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}

	inst := ctx.Step("deploy").UnknownInstance(addrs.WildcardKey)
	if inst == nil {
		t.Fatal("expected unknown wildcard for_each step instance")
	}
	if !inst.RepetitionData().EachKey.RawEquals(cty.UnknownVal(cty.String)) {
		t.Fatalf("wrong unknown wildcard each.key repetition data: %#v", inst.RepetitionData())
	}
}

func TestRunbookContextTracksStepDependencies(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "prepare" {
  output "result" {
    value = "ok"
  }
}

step "deploy" {
  output "result" {
    value = step.prepare.result
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	graph, buildDiags := (&RunbookPlanGraphBuilder{Context: ctx, Operation: runbookgraph.WalkValidate}).Build()
	if buildDiags.HasErrors() {
		t.Fatalf("unexpected build diagnostics: %s", buildDiags.Err())
	}
	deploy := graph.ConfigSteps["deploy"]
	var deployOutput *nodeRunbookStepOutput
	var prepareOutput *nodeRunbookStepOutput
	for _, raw := range graph.Graph.Vertices() {
		if n, ok := raw.(*nodeRunbookStepOutput); ok && n.Instance == nil && n.Step != nil && n.Step.Name() == "deploy" && n.Output != nil && n.Output.Name == "result" {
			deployOutput = n
		}
		if n, ok := raw.(*nodeRunbookStepOutput); ok && n.Instance == nil && n.Step != nil && n.Step.Name() == "prepare" && n.Output != nil && n.Output.Name == "result" {
			prepareOutput = n
		}
	}
	if deploy == nil || deployOutput == nil || prepareOutput == nil {
		t.Fatalf("missing step nodes in graph: %#v", graph.ConfigSteps)
	}
	outputDeps := graph.Graph.DownEdges(deployOutput)
	if !outputDeps.Include(prepareOutput) {
		t.Fatalf("expected deploy output to depend on prepare output, got %#v", outputDeps)
	}
}

func TestRunbookContextValidateRejectsStepDependencyCycle(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "prepare" {
  output "result" {
    value = step.deploy.result
  }
}

step "deploy" {
  output "result" {
    value = step.prepare.result
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
	if _, buildDiags := (&RunbookPlanGraphBuilder{Context: ctx, Operation: runbookgraph.WalkValidate}).Build(); !buildDiags.HasErrors() {
		t.Fatal("expected graph build diagnostics for cyclic graph")
	}
}

func TestRunbookPlanGraphBuilderBuildsCrossStepOutputEdges(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "prepare" {
  output "result" {
    value = "ok"
  }
}

step "deploy" {
  output "result" {
    value = step.prepare.result
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}

	graph, buildDiags := (&RunbookPlanGraphBuilder{Context: ctx, Operation: runbookgraph.WalkValidate}).Build()
	if buildDiags.HasErrors() {
		t.Fatalf("unexpected build diagnostics: %s", buildDiags.Err())
	}

	deploy := graph.ConfigSteps["deploy"]
	var deployOutput *nodeRunbookStepOutput
	var prepareOutput *nodeRunbookStepOutput
	for _, raw := range graph.Graph.Vertices() {
		if n, ok := raw.(*nodeRunbookStepOutput); ok && n.Instance == nil && n.Step != nil && n.Step.Name() == "deploy" && n.Output != nil && n.Output.Name == "result" {
			deployOutput = n
		}
		if n, ok := raw.(*nodeRunbookStepOutput); ok && n.Instance == nil && n.Step != nil && n.Step.Name() == "prepare" && n.Output != nil && n.Output.Name == "result" {
			prepareOutput = n
		}
	}
	if deploy == nil || deployOutput == nil || prepareOutput == nil {
		t.Fatalf("missing step nodes in graph: %#v", graph.ConfigSteps)
	}
	deps := graph.Graph.DownEdges(deploy)
	for _, dep := range deps {
		if dep == prepareOutput {
			return
		}
	}
	if deployOutput != nil {
		outputDeps := graph.Graph.DownEdges(deployOutput)
		for _, outputDep := range outputDeps {
			if outputDep == prepareOutput {
				return
			}
		}
	}
	t.Fatalf("expected deploy to depend on prepare, got %#v", deps)
}

func TestRunbookPlanGraphBuilderBuildsStableStepOrder(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "setup" {
  output "result" {
    value = "ok"
  }
}

step "prepare" {
  output "result" {
    value = step.setup.result
  }
}

step "verify" {
  output "result" {
    value = step.setup.result
  }
}

step "deploy" {
  output "result" {
    value = step.prepare.result != "" ? step.verify.result : ""
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	graph, buildDiags := (&RunbookPlanGraphBuilder{Context: ctx, Operation: runbookgraph.WalkValidate}).Build()
	if buildDiags.HasErrors() {
		t.Fatalf("unexpected build diagnostics: %s", buildDiags.Err())
	}
	var setupOutput, prepareOutput, verifyOutput, deployOutput *nodeRunbookStepOutput
	for _, raw := range graph.Graph.Vertices() {
		node, ok := raw.(*nodeRunbookStepOutput)
		if !ok || node.Instance != nil || node.Step == nil || node.Output == nil || node.Output.Name != "result" {
			continue
		}
		switch node.Step.Name() {
		case "setup":
			setupOutput = node
		case "prepare":
			prepareOutput = node
		case "verify":
			verifyOutput = node
		case "deploy":
			deployOutput = node
		}
	}
	if setupOutput == nil || prepareOutput == nil || verifyOutput == nil || deployOutput == nil {
		t.Fatalf("missing step nodes in graph: %#v", graph.ConfigSteps)
	}
	if !graph.Graph.Ancestors(prepareOutput).Include(setupOutput) {
		t.Fatalf("expected prepare output to depend on setup output")
	}
	if !graph.Graph.Ancestors(verifyOutput).Include(setupOutput) {
		t.Fatalf("expected verify output to depend on setup output")
	}
	deployAncestors := graph.Graph.Ancestors(deployOutput)
	if !deployAncestors.Include(prepareOutput) {
		t.Fatalf("expected deploy output to depend on prepare output")
	}
	if !deployAncestors.Include(verifyOutput) {
		t.Fatalf("expected deploy output to depend on verify output")
	}
}

func TestRunbookContextValidateMissingLocalReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  output "result" {
    value = local.greeting
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateMissingVariableReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
output "summary" {
  value = var.name
}

step "deploy" {}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateRejectsBareInStepActionReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  action "http" "notify" {}

  output "result" {
    value = action.http.notify
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateMissingInStepActionReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  output "result" {
    value = action.http.notify.output.id
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateMissingDataSourceReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  output "result" {
    value = data.aws_instance.example.id
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateMissingListReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  output "result" {
    value = list.aws_instance.example.ids
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateMissingLocalReferenceInLocal(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  locals {
    greeting = local.name
  }

  output "result" {
    value = "ok"
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateMissingVariableReferenceInStepCount(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  count = var.instance_count

  output "result" {
    value = "ok"
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateMissingVariableReferenceInActionCount(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  action "http" "notify" {
    count = var.instance_count
  }

  output "result" {
    value = "ok"
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateMissingVariableReferenceInListLimit(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  list "aws_instance" "example" {
    provider = aws
    limit = var.page_limit
  }

  output "result" {
    value = "ok"
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateRejectsCountOutsideCountedContext(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  output "result" {
    value = count.index
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateAllowsCountInsideCountedListLimit(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  list "aws_instance" "example" {
    provider = aws
    count = 2
    limit = count.index
  }

  output "result" {
    value = "ok"
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); diags.HasErrors() {
		t.Fatalf("unexpected validation diagnostics: %s", diags.Err())
	}
}

func TestRunbookContextValidateRejectsInvalidCountAttributeInListLimit(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  list "aws_instance" "example" {
    provider = aws
    count = 2
    limit = count.value
  }

  output "result" {
    value = "ok"
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateRejectsEachOutsideForEachContext(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  output "result" {
    value = each.key
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateAllowsEachInsideForEachListLimit(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  list "aws_instance" "example" {
    provider = aws
    for_each = toset(["a", "b"])
    limit = each.key != "" ? 1 : 0
  }

  output "result" {
    value = "ok"
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); diags.HasErrors() {
		t.Fatalf("unexpected validation diagnostics: %s", diags.Err())
	}
}

func TestRunbookContextValidateRejectsInvalidEachAttributeInListLimit(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "deploy" {
  list "aws_instance" "example" {
    provider = aws
    for_each = toset(["a", "b"])
    limit = each.index
  }

  output "result" {
    value = "ok"
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateRejectsMissingStepInstanceKeyForCountedStep(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "prepare" {
  count = 2

  output "result" {
    value = "ok"
  }
}

step "deploy" {
  output "result" {
    value = step.prepare.result
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateRejectsUnexpectedStepInstanceKey(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "prepare" {
  output "result" {
    value = "ok"
  }
}

step "deploy" {
  output "result" {
    value = step.prepare[0].result
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateRejectsWrongStepInstanceKeyTypeForCount(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "prepare" {
  count = 2

  output "result" {
    value = "ok"
  }
}

step "deploy" {
  output "result" {
    value = step.prepare["blue"].result
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateRejectsWrongStepInstanceKeyTypeForForEach(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "prepare" {
  for_each = toset(["blue", "green"])

  output "result" {
    value = "ok"
  }
}

step "deploy" {
  output "result" {
    value = step.prepare[0].result
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); !diags.HasErrors() {
		t.Fatal("expected validation diagnostics but got none")
	}
}

func TestRunbookContextValidateAllowsIndexedCountedStepReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "prepare" {
  count = 2

  output "result" {
    value = "ok"
  }
}

step "deploy" {
  output "result" {
    value = step.prepare[0].result
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); diags.HasErrors() {
		t.Fatalf("unexpected validation diagnostics: %s", diags.Err())
	}
}

func TestRunbookContextValidateAllowsKeyedForEachStepReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
step "prepare" {
  for_each = toset(["blue", "green"])

  output "result" {
    value = "ok"
  }
}

step "deploy" {
  output "result" {
    value = step.prepare["blue"].result
  }
}
`)

	parser := runbookconfig.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %s", diags.Error())
	}

	ctx, diags := NewContext(&RunbookContextOpts{Config: config})
	if diags.HasErrors() {
		t.Fatalf("unexpected context diagnostics: %s", diags.Error())
	}
	if diags := ctx.Validate(); diags.HasErrors() {
		t.Fatalf("unexpected validation diagnostics: %s", diags.Err())
	}
}

func writeTestFile(t *testing.T, fs afero.Fs, path, src string) {
	t.Helper()
	if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %s", path, err)
	}
}
