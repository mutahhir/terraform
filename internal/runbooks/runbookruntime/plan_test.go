// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"testing"

	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/runbooks/runbookgraph"
	"github.com/spf13/afero"
	"github.com/zclconf/go-cty/cty"
)

func TestRunbookContextPlanBuildsGraph(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "instance_count" {
  type    = number
  default = 2
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

	plan, planDiags := ctx.Plan(&PlanOpts{})
	if planDiags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}
	if plan == nil || plan.Graph == nil {
		t.Fatal("expected plan graph")
	}
	if len(plan.Graph.StepVertices) != 2 {
		t.Fatalf("expected 2 step vertices, got %#v", plan.Graph.StepVertices)
	}
}

func TestRunbookContextPlanUsesPlanTimeInputs(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "instance_count" {
  type    = number
  default = 1
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

	plan, planDiags := ctx.Plan(&PlanOpts{PlanTimeInputs: &PlanTimeInputs{
		Variables: map[string]cty.Value{
			"instance_count": cty.NumberIntVal(3),
		},
	}})
	if planDiags.HasErrors() {
		t.Fatalf("unexpected plan diagnostics: %s", planDiags.Err())
	}
	if plan == nil || plan.Graph == nil {
		t.Fatal("expected plan graph")
	}
	if len(plan.Graph.StepVertices) != 3 {
		t.Fatalf("expected 3 step vertices, got %#v", plan.Graph.StepVertices)
	}
}

func TestRunbookContextPlanRejectsUnknownStepRepetition(t *testing.T) {
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

	plan, planDiags := ctx.Plan(&PlanOpts{})
	if !planDiags.HasErrors() {
		t.Fatal("expected plan diagnostics but got none")
	}
	if plan != nil {
		t.Fatalf("expected nil plan, got %#v", plan)
	}
}

func TestRunbookContextPlanBuildsInstanceScopedInternalNodes(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "instance_count" {
  type    = number
  default = 2
}

step "deploy" {
  count = var.instance_count

  locals {
    greeting = "hello"
  }

  action "http" "notify" {
    config {
      message = local.greeting
    }
  }

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

	graph, buildDiags := (&RunbookPlanGraphBuilder{Context: ctx, Operation: runbookgraph.WalkPlan}).Build()
	if buildDiags.HasErrors() {
		t.Fatalf("unexpected build diagnostics: %s", buildDiags.Err())
	}
	walkDiags := runbookgraph.Walk(graph, newRunbookGraphWalker(ctx, graph, runbookgraph.WalkPlan))
	if walkDiags.HasErrors() {
		t.Fatalf("unexpected walk diagnostics: %s", walkDiags.Err())
	}

	if len(graph.StepVertices) != 2 {
		t.Fatalf("expected 2 step instance vertices, got %#v", graph.StepVertices)
	}
	for _, insts := range graph.InstancesByStep {
		if len(insts) != 2 {
			t.Fatalf("expected 2 instances by step, got %#v", graph.InstancesByStep)
		}
	}
	actionExpanders := 0
	for _, raw := range graph.Graph.Vertices() {
		if _, ok := raw.(*nodeExpandRunbookAction); ok {
			actionExpanders++
		}
	}
	if actionExpanders != 1 {
		t.Fatalf("expected 1 action expander node in root plan graph, got %d", actionExpanders)
	}
}

func TestRunbookContextPlanGraphRunbookOutputDependsOnStep(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/workspace/main.tf", ``)
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
output "summary" {
  value = step.deploy.result
}

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

	graph, buildDiags := (&RunbookPlanGraphBuilder{Context: ctx, Operation: runbookgraph.WalkValidate}).Build()
	if buildDiags.HasErrors() {
		t.Fatalf("unexpected build diagnostics: %s", buildDiags.Err())
	}

	var outputNode *nodeRunbookOutput
	for _, raw := range graph.Graph.Vertices() {
		if n, ok := raw.(*nodeRunbookOutput); ok && n.NameValue == "summary" {
			outputNode = n
			break
		}
	}
	if outputNode == nil {
		t.Fatal("expected runbook output node")
	}
	var stepOutputNode *nodeRunbookStepOutput
	for _, raw := range graph.Graph.Vertices() {
		if n, ok := raw.(*nodeRunbookStepOutput); ok && n.Instance == nil && n.Step != nil && n.Step.Name() == "deploy" && n.Output != nil && n.Output.Name == "result" {
			stepOutputNode = n
			break
		}
	}
	if stepOutputNode == nil {
		t.Fatal("expected deploy step output node")
	}
	deps := graph.Graph.DownEdges(outputNode)
	for _, dep := range deps {
		if dep == stepOutputNode {
			return
		}
	}
	t.Fatalf("expected output to depend on deploy step output node, got %#v", deps)
}
