// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
)

type StepTransformer struct {
	Context   *RunbookContext
	Operation walkOperation
}

type stepInternalVertices struct {
	locals      map[string]dag.Vertex
	actions     map[string]dag.Vertex
	dataSources map[string]dag.Vertex
	lists       map[string]dag.Vertex
	outputs     map[string]dag.Vertex
}

func (t *StepTransformer) Transform(g *PlanGraph) error {
	if t.Context == nil || g == nil || g.Graph == nil {
		return nil
	}

	stepNames := make([]string, 0, len(t.Context.config.Steps))
	for stepName := range t.Context.config.Steps {
		stepNames = append(stepNames, stepName)
	}
	sort.Strings(stepNames)
	internalByStep := make(map[string]*stepInternalVertices, len(stepNames))
	for _, stepName := range stepNames {
		step := t.Context.Step(stepName)
		if step == nil {
			continue
		}
		node := &nodeExpandRunbookStep{Step: step}
		g.ConfigSteps[stepName] = node
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(g.Root, node))
		if depVertex, ok := t.Context.stepVertices[stepName]; ok {
			t.Context.stepDependencyGraph.Add(depVertex)
		}
		internalByStep[stepName] = t.addInternalVertices(g, node, step)
	}

	for _, stepName := range stepNames {
		stepCfg := t.Context.config.Steps[stepName]
		internal := internalByStep[stepName]
		for _, depName := range t.stepDependencies(stepCfg) {
			from, fromOK := g.ConfigSteps[stepName]
			to, toOK := g.ConfigSteps[depName]
			if fromOK && toOK {
				g.Graph.Connect(dag.BasicEdge(from, to))
			}
			fromDep, fromDepOK := t.Context.stepVertices[stepName]
			toDep, toDepOK := t.Context.stepVertices[depName]
			if fromDepOK && toDepOK {
				t.Context.stepDependencyGraph.Connect(dag.BasicEdge(fromDep, toDep))
			}
		}
		t.connectInternalDependencies(g, stepCfg, internal)
	}

	return nil
}

func (t *StepTransformer) addInternalVertices(g *PlanGraph, stepNode *nodeExpandRunbookStep, step *Step) *stepInternalVertices {
	ret := &stepInternalVertices{
		locals:      map[string]dag.Vertex{},
		actions:     map[string]dag.Vertex{},
		dataSources: map[string]dag.Vertex{},
		lists:       map[string]dag.Vertex{},
		outputs:     map[string]dag.Vertex{},
	}
	if g == nil || g.Graph == nil || step == nil || step.Config() == nil {
		return ret
	}

	for _, local := range step.Config().Locals {
		node := &nodeRunbookStepLocal{Step: step, Local: local}
		ret.locals[local.Name] = node
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for _, action := range step.Config().Actions {
		node := &nodeRunbookAction{Step: step, Action: action}
		ret.actions[action.Addr().String()] = node
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for _, dataSource := range step.Config().DataSources {
		node := &nodeRunbookDataSource{Step: step, DataSource: dataSource}
		ret.dataSources[dataSource.Addr().String()] = node
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for _, list := range step.Config().ListResources {
		node := &nodeRunbookList{Step: step, List: list}
		ret.lists[list.Addr().String()] = node
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for _, output := range step.Config().Outputs {
		node := &nodeRunbookStepOutput{Step: step, Output: output}
		ret.outputs[output.Name] = node
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}

	return ret
}

func (t *StepTransformer) connectInternalDependencies(g *PlanGraph, step *runbookconfig.Step, internal *stepInternalVertices) {
	if g == nil || g.Graph == nil || step == nil || internal == nil {
		return
	}

	for _, local := range step.Locals {
		t.connectExpressionDependencies(g, internal.locals[local.Name], internal, local.Expr)
	}
	for _, action := range step.Actions {
		current := internal.actions[action.Addr().String()]
		t.connectExpressionDependencies(g, current, internal, action.Count, action.ForEach)
		t.connectBodyDependencies(g, current, internal, action.Config)
	}
	for _, dataSource := range step.DataSources {
		current := internal.dataSources[dataSource.Addr().String()]
		t.connectExpressionDependencies(g, current, internal, dataSource.Count, dataSource.ForEach)
		t.connectBodyDependencies(g, current, internal, dataSource.Config)
	}
	for _, list := range step.ListResources {
		current := internal.lists[list.Addr().String()]
		t.connectExpressionDependencies(g, current, internal, list.Count, list.ForEach)
		if list.List != nil {
			t.connectExpressionDependencies(g, current, internal, list.List.IncludeResource, list.List.Limit)
		}
		t.connectBodyDependencies(g, current, internal, list.Config)
	}
	for _, output := range step.Outputs {
		t.connectExpressionDependencies(g, internal.outputs[output.Name], internal, output.Expr)
	}
}

func (t *StepTransformer) connectBodyDependencies(g *PlanGraph, current dag.Vertex, internal *stepInternalVertices, body hcl.Body) {
	visitBodyExpressions(body, func(expr hcl.Expression) {
		t.connectExpressionDependencies(g, current, internal, expr)
	})
}

func (t *StepTransformer) connectExpressionDependencies(g *PlanGraph, current dag.Vertex, internal *stepInternalVertices, exprs ...hcl.Expression) {
	if g == nil || g.Graph == nil || current == nil || internal == nil {
		return
	}
	for _, expr := range exprs {
		if expr == nil {
			continue
		}
		for _, traversal := range expr.Variables() {
			if dep := t.internalDependencyVertex(traversal, internal); dep != nil && dep != current {
				g.Graph.Connect(dag.BasicEdge(current, dep))
			}
		}
	}
}

func (t *StepTransformer) internalDependencyVertex(traversal hcl.Traversal, internal *stepInternalVertices) dag.Vertex {
	if internal == nil || len(traversal) <= 1 {
		return nil
	}
	rootName := traversal.RootName()
	if rootName == "step" || rootName == "workspace" || rootName == "var" {
		return nil
	}

	ref, refDiags := runbookaddrs.ParseInStepReference(traversal)
	if refDiags.HasErrors() {
		return nil
	}

	switch addr := ref.Target.(type) {
	case addrs.LocalValue:
		return internal.locals[addr.Name]
	case runbookaddrs.ActionInstance:
		return internal.actions[addr.String()]
	case runbookaddrs.DataSource:
		return internal.dataSources[addr.String()]
	case runbookaddrs.List:
		return internal.lists[addr.String()]
	default:
		return nil
	}
}

func (t *StepTransformer) stepDependencies(step *runbookconfig.Step) []string {
	seen := map[string]struct{}{}
	visitExpr := func(expr hcl.Expression) {
		if expr == nil || step == nil {
			return
		}
		for _, traversal := range expr.Variables() {
			if traversal.RootName() != "step" {
				continue
			}
			ref, _, refDiags := runbookaddrs.ParseStepOutputReference(traversal)
			if refDiags.HasErrors() {
				continue
			}
			addr, ok := ref.Target.(runbookaddrs.StepOutputValue)
			if !ok {
				continue
			}
			depName := addr.ConfigStepOutputValue().Step.Name
			if depName == "" || depName == step.Name {
				continue
			}
			seen[depName] = struct{}{}
		}
	}

	visitExpr(step.Count)
	visitExpr(step.ForEach)
	for _, local := range step.Locals {
		visitExpr(local.Expr)
	}
	for _, action := range step.Actions {
		visitExpr(action.Count)
		visitExpr(action.ForEach)
		visitBodyExpressions(action.Config, visitExpr)
	}
	for _, dataSource := range step.DataSources {
		visitExpr(dataSource.Count)
		visitExpr(dataSource.ForEach)
		visitBodyExpressions(dataSource.Config, visitExpr)
	}
	for _, list := range step.ListResources {
		visitExpr(list.Count)
		visitExpr(list.ForEach)
		if list.List != nil {
			visitExpr(list.List.IncludeResource)
			visitExpr(list.List.Limit)
		}
		visitBodyExpressions(list.Config, visitExpr)
	}
	for _, output := range step.Outputs {
		visitExpr(output.Expr)
	}
	for _, execution := range step.Executions {
		for _, action := range execution.InvokeAction {
			if workspaceAction, ok := action.(runbookaddrs.WorkspaceActionInstance); ok {
				_ = workspaceAction
			}
		}
	}
	for _, condition := range step.Preconditions {
		visitExpr(condition.Condition)
		visitExpr(condition.ErrorMessage)
	}
	for _, condition := range step.Postconditions {
		visitExpr(condition.Condition)
		visitExpr(condition.ErrorMessage)
	}

	ret := make([]string, 0, len(seen))
	for name := range seen {
		ret = append(ret, name)
	}
	sort.Strings(ret)
	return ret
}
