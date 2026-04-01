// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"sort"

	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/runbooks/runbookgraph"
)

type StepTransformer struct {
	Context   *RunbookContext
	Operation runbookgraph.WalkOperation
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
	for _, stepName := range stepNames {
		step := t.Context.Step(stepName)
		if step == nil {
			continue
		}
		node := &nodeExpandRunbookStep{Step: step}
		g.ConfigSteps[stepName] = node
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(g.Root, node))
		t.addInternalVertices(g, node, step)
		addConfigBehaviorNodes(g, node, step)
	}

	return nil
}

func (t *StepTransformer) addInternalVertices(g *PlanGraph, stepNode *nodeExpandRunbookStep, step *Step) {
	if g == nil || g.Graph == nil || step == nil || step.Config() == nil {
		return
	}

	for _, local := range step.Config().Locals {
		node := &nodeExpandRunbookStepLocal{Step: step, Local: local}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for _, action := range step.Config().Actions {
		node := &nodeExpandRunbookAction{Step: step, Action: action}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for _, dataSource := range step.Config().DataSources {
		node := &nodeExpandRunbookDataSource{Step: step, DataSource: dataSource}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for _, list := range step.Config().ListResources {
		node := &nodeExpandRunbookList{Step: step, List: list}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for _, output := range step.Config().Outputs {
		node := &nodeRunbookStepOutput{Step: step, Output: output}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
}
