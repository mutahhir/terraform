// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
)

type StepTransformer struct {
	Context   *RunbookContext
	Operation walkOperation
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
		vertex := runbookStepVertex{Step: step}
		g.ConfigSteps[stepName] = vertex
		g.Graph.Add(vertex)
		g.Graph.Connect(dag.BasicEdge(g.Root, vertex))
		if depVertex, ok := t.Context.stepVertices[stepName]; ok {
			t.Context.stepDependencyGraph.Add(depVertex)
		}
	}

	for _, stepName := range stepNames {
		stepCfg := t.Context.config.Steps[stepName]
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
	}

	return nil
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
