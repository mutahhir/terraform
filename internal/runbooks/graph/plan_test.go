// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"testing"

	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/dag"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runtime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terraform"
)

func TestNewPlanBuildsVariableStepAndOutputNodes(t *testing.T) {
	graph, diags := NewPlan(&runbookconfigs.RunbookConfig{
		Variables: map[string]*configs.Variable{
			"input": {Name: "input"},
		},
		Steps: map[string]*runbookconfigs.Step{
			"discover": {Name: "discover"},
		},
		Outputs: map[string]*configs.Output{
			"result": {Name: "result"},
		},
	})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}

	inputNode := rootVariableNode(graph, "input")
	if inputNode == nil {
		t.Fatal("expected terraform root input variable node")
	}
	if inputNode.Config == nil || inputNode.Config.Name != "input" {
		t.Fatal("expected terraform root input variable node to carry config")
	}
	if !graph.HasVertex(&NodeStep{StepName: "discover"}) {
		t.Fatal("expected step node")
	}
	outputNode := outputNode(graph, "result")
	if outputNode == nil {
		t.Fatal("expected terraform output node")
	}
	if !hasVertexNamed(graph, "root") {
		t.Fatal("expected root node")
	}

	root := vertexNamed(graph, "root")
	rootDeps := graph.DownEdges(root)
	if !rootDeps.Include(inputNode) {
		t.Fatal("expected root to connect to input variable")
	}
	if !rootDeps.Include(&NodeStep{StepName: "discover"}) {
		t.Fatal("expected root to connect to step")
	}
	if !rootDeps.Include(outputNode) {
		t.Fatal("expected root to connect to output")
	}
}

func TestStepDetailsTransformerReplacesStepNodes(t *testing.T) {
	configStep := &runbookconfigs.Step{Name: "discover"}
	graph, diags := NewPlan(&runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"discover": configStep,
		},
	})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}

	runtimeStep := &runtime.Step{Name: "discover", Config: configStep}
	if err := (&StepDetailsTransformer{
		Config: &runbookconfigs.RunbookConfig{
			Steps: map[string]*runbookconfigs.Step{
				"discover": configStep,
			},
		},
		Steps: map[string]*runtime.Step{
			"discover": runtimeStep,
		},
	}).Transform(graph); err != nil {
		t.Fatalf("unexpected transform error: %s", err)
	}

	if graph.HasVertex(&NodeStep{StepName: "discover"}) {
		t.Fatal("expected bare step node to be replaced")
	}

	details := &NodeStepDetails{StepName: "discover"}
	if !graph.HasVertex(details) {
		t.Fatal("expected detailed step node")
	}

	var found *NodeStepDetails
	for _, vertex := range graph.Vertices() {
		node, ok := vertex.(*NodeStepDetails)
		if ok && node.StepName == "discover" {
			found = node
			break
		}
	}
	if found == nil {
		t.Fatal("expected to find detailed step node")
	}
	if found.Config != configStep {
		t.Fatal("expected detailed node to keep config")
	}
	if found.Step != runtimeStep {
		t.Fatal("expected detailed node to keep runtime step")
	}
}

func TestPlanBuilderSteps(t *testing.T) {
	builder := &PlanBuilder{}
	steps := builder.Steps()

	if len(steps) != 5 {
		t.Fatalf("expected 5 build steps, got %d", len(steps))
	}

	if _, ok := steps[0].(*terraform.RootVariableTransformer); !ok {
		t.Fatal("expected terraform root variable transformer first")
	}
	if _, ok := steps[1].(*PlanStepTransformer); !ok {
		t.Fatal("expected steps transformer second")
	}
	if outputTransformer, ok := steps[2].(*terraform.OutputTransformer); !ok {
		t.Fatal("expected terraform output transformer third")
	} else if !outputTransformer.Planning {
		t.Fatal("expected terraform output transformer to be in planning mode")
	}
	if _, ok := steps[3].(*terraform.RootTransformer); !ok {
		t.Fatal("expected terraform root transformer fourth")
	}
	if _, ok := steps[4].(*terraform.TransitiveReductionTransformer); !ok {
		t.Fatal("expected terraform transitive reduction transformer fifth")
	}

	builder.StepsRuntime = map[string]*runtime.Step{"discover": {Name: "discover"}}
	steps = builder.Steps()
	if len(steps) != 6 {
		t.Fatalf("expected 6 build steps when runtime steps are provided, got %d", len(steps))
	}
	if _, ok := steps[4].(*StepDetailsTransformer); !ok {
		t.Fatal("expected step details transformer before reduction")
	}
	if _, ok := steps[5].(*terraform.TransitiveReductionTransformer); !ok {
		t.Fatal("expected terraform transitive reduction transformer last")
	}
}

func TestPlanBuilderBuildIncludesStepDetailsTransformer(t *testing.T) {
	configStep := &runbookconfigs.Step{Name: "discover"}
	runtimeStep := &runtime.Step{Name: "discover", Config: configStep}

	graph, diags := (&PlanBuilder{
		Config: &runbookconfigs.RunbookConfig{
			Steps: map[string]*runbookconfigs.Step{
				"discover": configStep,
			},
		},
		StepsRuntime: map[string]*runtime.Step{
			"discover": runtimeStep,
		},
	}).Build()
	if diags.HasErrors() {
		t.Fatalf("unexpected build diagnostics: %s", diags.Err())
	}

	if !graph.HasVertex(&NodeStepDetails{StepName: "discover"}) {
		t.Fatal("expected builder to include detailed step node")
	}
	if graph.HasVertex(&NodeStep{StepName: "discover"}) {
		t.Fatal("expected builder to replace bare step node when runtime steps are provided")
	}
}

func TestRootRunbookConfigBuildsTerraformConfig(t *testing.T) {
	variable := &configs.Variable{Name: "input"}
	output := &configs.Output{Name: "result"}
	config := rootRunbookConfig(&runbookconfigs.RunbookConfig{
		Variables: map[string]*configs.Variable{
			"input": variable,
		},
		Outputs: map[string]*configs.Output{
			"result": output,
		},
	})
	if config == nil || config.Module == nil {
		t.Fatal("expected terraform config")
	}
	if config.Root != config {
		t.Fatal("expected root config to be self-referential")
	}
	if config.Module.Variables["input"] != variable {
		t.Fatal("expected variable to be exposed through terraform config")
	}
	if config.Module.Outputs["result"] != output {
		t.Fatal("expected output to be exposed through terraform config")
	}
}

func TestTerraformTransitiveReductionRemovesRedundantEdges(t *testing.T) {
	graph := &terraform.Graph{}
	a := &NodeStep{StepName: "a"}
	b := &NodeStep{StepName: "b"}
	c := &NodeStep{StepName: "c"}

	graph.Add(a)
	graph.Add(b)
	graph.Add(c)
	graph.Connect(dag.BasicEdge(a, b))
	graph.Connect(dag.BasicEdge(b, c))
	graph.Connect(dag.BasicEdge(a, c))

	if err := (&terraform.TransitiveReductionTransformer{}).Transform(graph); err != nil {
		t.Fatalf("unexpected transform error: %s", err)
	}

	if !graph.DownEdges(a).Include(b) {
		t.Fatal("expected direct edge from a to b to remain")
	}
	if !graph.DownEdges(b).Include(c) {
		t.Fatal("expected direct edge from b to c to remain")
	}
	if graph.DownEdges(a).Include(c) {
		t.Fatal("expected transitive edge from a to c to be removed")
	}
}

func hasVertexNamed(g *terraform.Graph, name string) bool {
	return vertexNamed(g, name) != nil
}

func rootVariableNode(g *terraform.Graph, name string) *terraform.NodeRootVariable {
	for _, vertex := range g.Vertices() {
		node, ok := vertex.(*terraform.NodeRootVariable)
		if ok && node.Addr.Name == name {
			return node
		}
	}
	return nil
}

func outputNode(g *terraform.Graph, name string) dag.Vertex {
	for _, vertex := range g.Vertices() {
		if dag.VertexName(vertex) == "output."+name+" (expand)" {
			return vertex
		}
	}
	return nil
}

func vertexNamed(g *terraform.Graph, name string) dag.Vertex {
	for _, vertex := range g.Vertices() {
		if dag.VertexName(vertex) == name {
			return vertex
		}
	}
	return nil
}
