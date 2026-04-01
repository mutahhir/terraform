// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"reflect"
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
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
	if diags := ctx.Validate(); diags.HasErrors() {
		t.Fatalf("unexpected validation diagnostics: %s", diags.Err())
	}
	deps := ctx.StepDependencies("deploy")
	if len(deps) != 1 || deps[0] == nil || deps[0].Name() != "prepare" {
		t.Fatalf("wrong step dependencies\ngot:  %#v", deps)
	}
	if len(ctx.StepDependencies("prepare")) != 0 {
		t.Fatalf("expected no dependencies for prepare, got %#v", ctx.StepDependencies("prepare"))
	}
	order := ctx.StepExecutionOrder()
	if len(order) != 2 {
		t.Fatalf("expected 2 steps in execution order, got %#v", order)
	}
	positions := make(map[string]int, len(order))
	for i, step := range order {
		positions[step.Name()] = i
	}
	if positions["prepare"] > positions["deploy"] {
		t.Fatalf("prepare should come before deploy, got %#v", order)
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
	if got := ctx.StepExecutionOrder(); got != nil {
		t.Fatalf("expected nil step execution order for cyclic graph, got %#v", got)
	}
}

func TestRunbookContextStepExecutionOrderStable(t *testing.T) {
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
	if diags := ctx.Validate(); diags.HasErrors() {
		t.Fatalf("unexpected validation diagnostics: %s", diags.Err())
	}

	order := ctx.StepExecutionOrder()
	if len(order) != 4 {
		t.Fatalf("expected 4 steps in execution order, got %#v", order)
	}

	positions := make(map[string]int, len(order))
	for i, step := range order {
		positions[step.Name()] = i
	}
	if positions["setup"] > positions["prepare"] {
		t.Fatalf("setup should come before prepare, got %#v", order)
	}
	if positions["setup"] > positions["verify"] {
		t.Fatalf("setup should come before verify, got %#v", order)
	}
	if positions["prepare"] > positions["deploy"] {
		t.Fatalf("prepare should come before deploy, got %#v", order)
	}
	if positions["verify"] > positions["deploy"] {
		t.Fatalf("verify should come before deploy, got %#v", order)
	}

	secondOrder := ctx.StepExecutionOrder()
	if len(secondOrder) != len(order) {
		t.Fatalf("expected repeated execution order call to return same number of steps\nfirst:  %#v\nsecond: %#v", order, secondOrder)
	}
	secondPositions := make(map[string]int, len(secondOrder))
	for i, step := range secondOrder {
		secondPositions[step.Name()] = i
	}
	if secondPositions["setup"] > secondPositions["prepare"] {
		t.Fatalf("setup should come before prepare on repeated call, got %#v", secondOrder)
	}
	if secondPositions["setup"] > secondPositions["verify"] {
		t.Fatalf("setup should come before verify on repeated call, got %#v", secondOrder)
	}
	if secondPositions["prepare"] > secondPositions["deploy"] {
		t.Fatalf("prepare should come before deploy on repeated call, got %#v", secondOrder)
	}
	if secondPositions["verify"] > secondPositions["deploy"] {
		t.Fatalf("verify should come before deploy on repeated call, got %#v", secondOrder)
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
