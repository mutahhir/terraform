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
	if !graph.HasVertex(&NodeExpandStep{StepName: "discover"}) {
		t.Fatal("expected step expansion node")
	}
	outputNode := &NodeOutputVariable{Output: &configs.Output{Name: "result"}}
	if !graph.HasVertex(outputNode) {
		t.Fatal("expected output node")
	}
	if !hasVertexNamed(graph, "root") {
		t.Fatal("expected root node")
	}

	root := vertexNamed(graph, "root")
	rootDeps := graph.DownEdges(root)
	if !rootDeps.Include(inputNode) {
		t.Fatal("expected root to connect to input variable")
	}
	if !rootDeps.Include(&NodeExpandStep{StepName: "discover"}) {
		t.Fatal("expected root to connect to step expansion node")
	}
	if !rootDeps.Include(outputNode) {
		t.Fatal("expected root to connect to output")
	}
}

func TestNodeExpandStepDynamicExpandCreatesStepInstance(t *testing.T) {
	configStep := &runbookconfigs.Step{Name: "discover"}
	node := &NodeExpandStep{
		StepName: "discover",
		Config:   configStep,
		Runtime:  &runtime.Step{Name: "discover", Config: configStep},
	}

	graph, diags := node.DynamicExpand(nil)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if graph == nil {
		t.Fatal("expected expanded subgraph")
	}
	if !graph.HasVertex(&NodeStepInstance{StepName: "discover"}) {
		t.Fatal("expected step instance node in expanded graph")
	}
	if !hasVertexNamed(graph, "root") {
		t.Fatal("expected rooted expanded subgraph")
	}

	var found *NodeStepInstance
	for _, vertex := range graph.Vertices() {
		node, ok := vertex.(*NodeStepInstance)
		if ok && node.StepName == "discover" {
			found = node
			break
		}
	}
	if found == nil {
		t.Fatal("expected to find step instance node")
	}
	if found.Config != configStep {
		t.Fatal("expected step instance to keep config")
	}
	if found.Runtime == nil || found.Runtime.Config != configStep {
		t.Fatal("expected step instance to keep runtime details")
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
	if _, ok := steps[2].(*PlanOutputTransformer); !ok {
		t.Fatal("expected output transformer third")
	}
	if _, ok := steps[3].(*terraform.RootTransformer); !ok {
		t.Fatal("expected terraform root transformer fourth")
	}
	if _, ok := steps[4].(*terraform.TransitiveReductionTransformer); !ok {
		t.Fatal("expected terraform transitive reduction transformer fifth")
	}

	builder.StepsRuntime = map[string]*runtime.Step{"discover": {Name: "discover"}}
	steps = builder.Steps()
	if len(steps) != 5 {
		t.Fatalf("expected runtime steps not to change build step count yet, got %d", len(steps))
	}
}

func TestPlanBuilderBuildIncludesStepExpansionNode(t *testing.T) {
	configStep := &runbookconfigs.Step{Name: "discover"}
	graph, diags := (&PlanBuilder{
		Config: &runbookconfigs.RunbookConfig{
			Steps: map[string]*runbookconfigs.Step{
				"discover": configStep,
			},
		},
		StepsRuntime: map[string]*runtime.Step{
			"discover": {Name: "discover", Config: configStep},
		},
	}).Build()
	if diags.HasErrors() {
		t.Fatalf("unexpected build diagnostics: %s", diags.Err())
	}

	if !graph.HasVertex(&NodeExpandStep{StepName: "discover"}) {
		t.Fatal("expected builder to include step expansion node")
	}
}

func TestRootVariableConfigBuildsTerraformConfig(t *testing.T) {
	variable := &configs.Variable{Name: "input"}
	config := rootVariableConfig(&runbookconfigs.RunbookConfig{
		Variables: map[string]*configs.Variable{
			"input": variable,
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
	if len(config.Module.Outputs) != 0 {
		t.Fatal("expected variable adapter to stay scoped to variables")
	}
	if len(config.Module.ProviderConfigs) != 0 {
		t.Fatal("expected variable adapter to stay scoped to variables")
	}
}

func TestTerraformTransitiveReductionRemovesRedundantEdges(t *testing.T) {
	graph := &terraform.Graph{}
	a := &NodeExpandStep{StepName: "a"}
	b := &NodeExpandStep{StepName: "b"}
	c := &NodeExpandStep{StepName: "c"}

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

func vertexNamed(g *terraform.Graph, name string) dag.Vertex {
	for _, vertex := range g.Vertices() {
		if dag.VertexName(vertex) == name {
			return vertex
		}
	}
	return nil
}
