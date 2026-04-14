// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
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

func TestNodeExpandStepDynamicExpandIncludesInnerContentNodes(t *testing.T) {
	step := &runbookconfigs.Step{
		Name:           "deploy",
		Actions:        []*configs.Action{{Type: "shell", Name: "run"}},
		DataSources:    []*configs.Resource{{Type: "server", Name: "selected"}},
		ListResources:  []*configs.Resource{{Type: "server", Name: "all"}},
		Locals:         []*configs.Local{{Name: "region"}},
		Executions:     []*runbookconfigs.Execution{{}},
		Preconditions:  []*runbookconfigs.Condition{{Kind: runbookconfigs.PreconditionCondition, DeclRange: hcl.Range{Filename: "test.hcl", Start: hcl.Pos{Line: 1}, End: hcl.Pos{Line: 1, Column: 10}}}},
		Postconditions: []*runbookconfigs.Condition{{Kind: runbookconfigs.PostconditionCondition, DeclRange: hcl.Range{Filename: "test.hcl", Start: hcl.Pos{Line: 2}, End: hcl.Pos{Line: 2, Column: 10}}}},
		Outputs:        []*configs.Output{{Name: "result"}},
	}

	graph, diags := (&NodeExpandStep{StepName: "deploy", Config: step}).DynamicExpand(nil)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}

	instance := &NodeStepInstance{StepName: "deploy"}
	if !graph.HasVertex(instance) {
		t.Fatal("expected step instance node")
	}

	children := graph.DownEdges(instance)
	if !children.Include(&NodeStepAction{StepName: "deploy", Action: &configs.Action{Type: "shell", Name: "run"}}) {
		t.Fatal("expected step action node")
	}
	if !children.Include(&NodeStepData{StepName: "deploy", Data: &configs.Resource{Type: "server", Name: "selected"}}) {
		t.Fatal("expected step data node")
	}
	if !children.Include(&NodeStepList{StepName: "deploy", List: &configs.Resource{Type: "server", Name: "all"}}) {
		t.Fatal("expected step list node")
	}
	if !children.Include(&NodeStepLocal{StepName: "deploy", Local: &configs.Local{Name: "region"}}) {
		t.Fatal("expected step local node")
	}
	if !children.Include(&NodeStepExecution{StepName: "deploy", Index: 0}) {
		t.Fatal("expected step execution node")
	}
	if !children.Include(&NodeStepOutput{StepName: "deploy", Output: &configs.Output{Name: "result"}}) {
		t.Fatal("expected step output node")
	}

	preFound := false
	postFound := false
	for _, vertex := range graph.Vertices() {
		condition, ok := vertex.(*NodeStepCondition)
		if !ok || condition.StepName != "deploy" {
			continue
		}
		if condition.Condition.Kind == runbookconfigs.PreconditionCondition {
			preFound = true
		}
		if condition.Condition.Kind == runbookconfigs.PostconditionCondition {
			postFound = true
		}
	}
	if !preFound {
		t.Fatal("expected precondition node")
	}
	if !postFound {
		t.Fatal("expected postcondition node")
	}
}

func TestNodeExpandStepDynamicExpandConnectsInnerReferences(t *testing.T) {
	step := &runbookconfigs.Step{
		Name:        "deploy",
		Locals:      []*configs.Local{{Name: "region", Expr: mustParseExpression(t, `data.server.selected.id`)}},
		DataSources: []*configs.Resource{{Type: "server", Name: "selected"}},
		Actions:     []*configs.Action{{Type: "shell", Name: "run", Config: mustParseBody(t, `value = local.region`)}},
		Executions:  []*runbookconfigs.Execution{{InvokeAction: []hcl.Traversal{mustParseTraversal(t, `action.shell.run`)}}},
		Outputs:     []*configs.Output{{Name: "result", Expr: mustParseExpression(t, `local.region`)}},
	}

	graph, diags := (&NodeExpandStep{StepName: "deploy", Config: step}).DynamicExpand(nil)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}

	localNode := &NodeStepLocal{StepName: "deploy", Local: &configs.Local{Name: "region"}}
	dataNode := &NodeStepData{StepName: "deploy", Data: &configs.Resource{Type: "server", Name: "selected"}}
	actionNode := &NodeStepAction{StepName: "deploy", Action: &configs.Action{Type: "shell", Name: "run"}}
	executionNode := &NodeStepExecution{StepName: "deploy", Index: 0}
	outputNode := &NodeStepOutput{StepName: "deploy", Output: &configs.Output{Name: "result"}}

	if !graph.DownEdges(localNode).Include(dataNode) {
		t.Fatalf("expected local to depend on referenced data, got: %#v", graph.DownEdges(localNode).List())
	}
	if !graph.DownEdges(actionNode).Include(localNode) {
		t.Fatal("expected action to depend on referenced local")
	}
	if !graph.DownEdges(executionNode).Include(actionNode) {
		t.Fatal("expected execution to depend on referenced action")
	}
	if !graph.DownEdges(outputNode).Include(localNode) {
		t.Fatal("expected output to depend on referenced local")
	}
}

func TestNodeExpandStepDynamicExpandMakesPostconditionsDependOnExecute(t *testing.T) {
	postcondition := &runbookconfigs.Condition{
		Kind:      runbookconfigs.PostconditionCondition,
		DeclRange: hcl.Range{Filename: "test.hcl", Start: hcl.Pos{Line: 4}, End: hcl.Pos{Line: 4, Column: 10}},
		Condition: mustParseExpression(t, `step.result`),
	}
	step := &runbookconfigs.Step{
		Name:           "deploy",
		Executions:     []*runbookconfigs.Execution{{}},
		Postconditions: []*runbookconfigs.Condition{postcondition},
		Outputs:        []*configs.Output{{Name: "result", Expr: mustParseExpression(t, `"done"`)}},
	}

	graph, diags := (&NodeExpandStep{StepName: "deploy", Config: step}).DynamicExpand(nil)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}

	executionNode := &NodeStepExecution{StepName: "deploy", Index: 0}
	postconditionNode := &NodeStepCondition{StepName: "deploy", Condition: postcondition}
	if !graph.DownEdges(postconditionNode).Include(executionNode) {
		t.Fatal("expected postcondition to depend on execute")
	}
}

func TestPlanBuilderSteps(t *testing.T) {
	builder := &PlanBuilder{}
	steps := builder.Steps()

	if len(steps) != 6 {
		t.Fatalf("expected 6 build steps, got %d", len(steps))
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
	if _, ok := steps[3].(*StepOutputReferenceTransformer); !ok {
		t.Fatal("expected step output reference transformer fourth")
	}
	if _, ok := steps[4].(*terraform.RootTransformer); !ok {
		t.Fatal("expected terraform root transformer fifth")
	}
	if _, ok := steps[5].(*terraform.TransitiveReductionTransformer); !ok {
		t.Fatal("expected terraform transitive reduction transformer sixth")
	}

	builder.StepsRuntime = map[string]*runtime.Step{"discover": {Name: "discover"}}
	steps = builder.Steps()
	if len(steps) != 6 {
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

func TestCrossStepOutputReferenceTransformerConnectsConsumerOutputToProducerOutput(t *testing.T) {
	t.Skip("TODO: replace coarse step-level edges with direct cross-step output-vertex edges")

	graph, diags := NewPlan(&runbookconfigs.RunbookConfig{
		Variables: map[string]*configs.Variable{
			"input": {Name: "input"},
		},
		Steps: map[string]*runbookconfigs.Step{
			"producer": {
				Name:    "producer",
				Outputs: []*configs.Output{{Name: "result", Expr: mustParseExpression(t, `var.input`)}},
			},
			"consumer": {
				Name:    "consumer",
				Outputs: []*configs.Output{{Name: "final", Expr: mustParseExpression(t, `step.producer.result`)}},
			},
		},
	})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}

	consumer := &NodeStepOutput{StepName: "consumer", Output: &configs.Output{Name: "final"}}
	producer := &NodeStepOutput{StepName: "producer", Output: &configs.Output{Name: "result"}}
	if !graph.DownEdges(consumer).Include(producer) {
		t.Fatal("expected consumer output to depend on referenced producer output")
	}
}

func TestCrossStepOutputReferenceTransformerConnectsConsumerLocalToProducerOutput(t *testing.T) {
	t.Skip("TODO: replace coarse step-level edges with direct cross-step output-vertex edges")

	graph, diags := NewPlan(&runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"producer": {
				Name:    "producer",
				Outputs: []*configs.Output{{Name: "result", Expr: mustParseExpression(t, `"ok"`)}},
			},
			"consumer": {
				Name:   "consumer",
				Locals: []*configs.Local{{Name: "copied", Expr: mustParseExpression(t, `steps.producer.result`)}},
			},
		},
	})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}

	consumer := &NodeStepLocal{StepName: "consumer", Local: &configs.Local{Name: "copied"}}
	producer := &NodeStepOutput{StepName: "producer", Output: &configs.Output{Name: "result"}}
	if !graph.DownEdges(consumer).Include(producer) {
		t.Fatal("expected consumer local to depend on referenced producer output")
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

func mustParseExpression(t *testing.T, src string) hcl.Expression {
	t.Helper()
	expr, diags := hclsyntax.ParseExpression([]byte(src), "test.hcl", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		t.Fatalf("parse expression %q: %s", src, diags.Error())
	}
	return expr
}

func mustParseTraversal(t *testing.T, src string) hcl.Traversal {
	t.Helper()
	trav, diags := hcl.AbsTraversalForExpr(mustParseExpression(t, src))
	if diags.HasErrors() {
		t.Fatalf("parse traversal %q: %s", src, diags.Error())
	}
	return trav
}

func mustParseBody(t *testing.T, src string) hcl.Body {
	t.Helper()
	file, diags := hclsyntax.ParseConfig([]byte(src), "test.hcl", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		t.Fatalf("parse body %q: %s", src, diags.Error())
	}
	return file.Body
}
