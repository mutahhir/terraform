package runbookgraph

import (
	"testing"

	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/terraform"
)

func TestComputeExecutionLayers_Sequential(t *testing.T) {
	g := &terraform.Graph{}

	a := &NodeExpandStep{StepName: "step_a"}
	b := &NodeExpandStep{StepName: "step_b"}
	c := &NodeExpandStep{StepName: "step_c"}

	g.Add(a)
	g.Add(b)
	g.Add(c)

	// b depends on a, c depends on b → fully sequential
	g.Connect(dag.BasicEdge(b, a))
	g.Connect(dag.BasicEdge(c, b))

	layers := ComputeExecutionLayers(g)

	if len(layers) != 3 {
		t.Fatalf("expected 3 layers, got %d", len(layers))
	}
	for i, layer := range layers {
		if len(layer.Steps) != 1 {
			t.Fatalf("layer %d: expected 1 step, got %d", i, len(layer.Steps))
		}
		if layer.IsParallel() {
			t.Fatalf("layer %d: should not be parallel", i)
		}
	}
	if layers[0].Steps[0].Name != "step_a" {
		t.Fatalf("expected step_a at layer 0, got %s", layers[0].Steps[0].Name)
	}
	if layers[1].Steps[0].Name != "step_b" {
		t.Fatalf("expected step_b at layer 1, got %s", layers[1].Steps[0].Name)
	}
	if layers[2].Steps[0].Name != "step_c" {
		t.Fatalf("expected step_c at layer 2, got %s", layers[2].Steps[0].Name)
	}
}

func TestComputeExecutionLayers_Parallel(t *testing.T) {
	g := &terraform.Graph{}

	a := &NodeExpandStep{StepName: "step_a"}
	b := &NodeExpandStep{StepName: "step_b"}
	c := &NodeExpandStep{StepName: "step_c"}

	g.Add(a)
	g.Add(b)
	g.Add(c)

	// No edges between them → all parallel at depth 0
	layers := ComputeExecutionLayers(g)

	if len(layers) != 1 {
		t.Fatalf("expected 1 layer, got %d", len(layers))
	}
	if !layers[0].IsParallel() {
		t.Fatalf("expected parallel layer")
	}
	if len(layers[0].Steps) != 3 {
		t.Fatalf("expected 3 steps in layer, got %d", len(layers[0].Steps))
	}
}

func TestComputeExecutionLayers_Diamond(t *testing.T) {
	g := &terraform.Graph{}

	init := &NodeExpandStep{StepName: "init"}
	frontend := &NodeExpandStep{StepName: "frontend"}
	backend := &NodeExpandStep{StepName: "backend"}
	deploy := &NodeExpandStep{StepName: "deploy"}

	g.Add(init)
	g.Add(frontend)
	g.Add(backend)
	g.Add(deploy)

	// frontend and backend depend on init, deploy depends on both
	g.Connect(dag.BasicEdge(frontend, init))
	g.Connect(dag.BasicEdge(backend, init))
	g.Connect(dag.BasicEdge(deploy, frontend))
	g.Connect(dag.BasicEdge(deploy, backend))

	layers := ComputeExecutionLayers(g)

	if len(layers) != 3 {
		t.Fatalf("expected 3 layers, got %d", len(layers))
	}

	// Layer 0: init (sequential)
	if len(layers[0].Steps) != 1 || layers[0].Steps[0].Name != "init" {
		t.Fatalf("layer 0: expected [init], got %v", layers[0].Steps)
	}

	// Layer 1: frontend + backend (parallel)
	if !layers[1].IsParallel() {
		t.Fatalf("layer 1: expected parallel")
	}
	if len(layers[1].Steps) != 2 {
		t.Fatalf("layer 1: expected 2 steps, got %d", len(layers[1].Steps))
	}

	// Layer 2: deploy (sequential)
	if len(layers[2].Steps) != 1 || layers[2].Steps[0].Name != "deploy" {
		t.Fatalf("layer 2: expected [deploy], got %v", layers[2].Steps)
	}
}

func TestComputeExecutionLayers_IgnoresNonStepNodes(t *testing.T) {
	g := &terraform.Graph{}

	a := &NodeExpandStep{StepName: "step_a"}
	// Use a root node (string vertex) to represent a non-step node
	g.Add(a)
	g.Add("root")
	g.Connect(dag.BasicEdge("root", a))

	layers := ComputeExecutionLayers(g)

	if len(layers) != 1 {
		t.Fatalf("expected 1 layer (only step nodes), got %d", len(layers))
	}
	if layers[0].Steps[0].Name != "step_a" {
		t.Fatalf("expected step_a, got %s", layers[0].Steps[0].Name)
	}
}
