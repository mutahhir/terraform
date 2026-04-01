// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/instances"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookgraph"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type nodeExpandRunbookStepLocal struct {
	Step     *Step
	Instance *StepInstance
	Local    *configs.Local
}

func (n *nodeExpandRunbookStepLocal) Hashcode() interface{} {
	if n.Local == nil {
		return nil
	}
	return fmt.Sprintf("%s.local.%s", stepNodePrefix(n.Step, n.Instance), n.Local.Name)
}

func (n *nodeExpandRunbookStepLocal) Scope() runbookgraph.Scope {
	return stepNodeScope(n.Step, n.Instance)
}

func (n *nodeExpandRunbookStepLocal) ReferenceableAddrs() []runbookgraph.ReferenceTarget {
	return []runbookgraph.ReferenceTarget{addrs.LocalValue{Name: n.Local.Name}}
}

func (n *nodeExpandRunbookStepLocal) References() []runbookgraph.Reference {
	return runbookReferencesInExpr(n.Scope(), n.Local.Expr)
}

func (n *nodeExpandRunbookStepLocal) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Data == nil || w.Data.Context == nil || n.Step == nil {
		return nil
	}
	return w.Data.Context.validateStepLocal(n.Step.Name(), n.Local)
}

type nodeRunbookStepLocal struct {
	Step     *Step
	Instance *StepInstance
	Local    *configs.Local
}

func (n *nodeRunbookStepLocal) Hashcode() interface{} {
	if n.Local == nil {
		return nil
	}
	return fmt.Sprintf("%s.local.%s.instance", stepNodePrefix(n.Step, n.Instance), n.Local.Name)
}

func (n *nodeRunbookStepLocal) Scope() runbookgraph.Scope {
	return stepNodeScope(n.Step, n.Instance)
}

func (n *nodeRunbookStepLocal) ReferenceableAddrs() []runbookgraph.ReferenceTarget {
	return []runbookgraph.ReferenceTarget{addrs.LocalValue{Name: n.Local.Name}}
}

func (n *nodeRunbookStepLocal) References() []runbookgraph.Reference {
	return runbookReferencesInExpr(n.Scope(), n.Local.Expr)
}

func (n *nodeRunbookStepLocal) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Data == nil || w.Data.Context == nil || n.Step == nil {
		return nil
	}
	return w.Data.Context.validateStepLocal(n.Step.Name(), n.Local)
}

type nodeExpandRunbookAction struct {
	Step     *Step
	Instance *StepInstance
	Action   *configs.Action
}

func (n *nodeExpandRunbookAction) Hashcode() interface{} {
	if n.Action == nil {
		return nil
	}
	return fmt.Sprintf("%s.action.%s", stepNodePrefix(n.Step, n.Instance), n.Action.Addr().String())
}

func (n *nodeExpandRunbookAction) Scope() runbookgraph.Scope {
	return stepNodeScope(n.Step, n.Instance)
}

func (n *nodeExpandRunbookAction) ReferenceableAddrs() []runbookgraph.ReferenceTarget {
	addr := n.Action.Addr()
	return []runbookgraph.ReferenceTarget{runbookaddrs.ActionInstance{Type: addr.Type, Name: addr.Name}}
}

func (n *nodeExpandRunbookAction) References() []runbookgraph.Reference {
	refs := runbookReferencesInExpr(n.Scope(), n.Action.Count, n.Action.ForEach)
	visitBodyExpressions(n.Action.Config, func(expr hcl.Expression) {
		refs = append(refs, runbookReferencesInExpr(n.Scope(), expr)...)
	})
	return refs
}

func (n *nodeExpandRunbookAction) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Data == nil || w.Data.Context == nil || n.Step == nil {
		return nil
	}
	return w.Data.Context.validateStepAction(n.Step.Name(), n.Action)
}

func (n *nodeExpandRunbookAction) DynamicExpand(w *RunbookGraphWalker) (*PlanGraph, tfdiags.Diagnostics) {
	return expandStepActionNode(w, n.Step, n.Instance, n.Action)
}

type nodeRunbookActionInstance struct {
	Step        *Step
	StepInst    *StepInstance
	Action      *configs.Action
	InstanceKey addrs.InstanceKey
	Repetition  instances.RepetitionData
}

func (n *nodeRunbookActionInstance) Hashcode() interface{} {
	return fmt.Sprintf("%s.action.%s%s", stepNodePrefix(n.Step, n.StepInst), n.Action.Addr().String(), instanceKeySuffix(n.InstanceKey))
}

func (n *nodeRunbookActionInstance) Scope() runbookgraph.Scope {
	return stepNodeScope(n.Step, n.StepInst)
}

func (n *nodeRunbookActionInstance) ReferenceableAddrs() []runbookgraph.ReferenceTarget {
	addr := n.Action.Addr()
	return []runbookgraph.ReferenceTarget{runbookaddrs.ActionInstance{Type: addr.Type, Name: addr.Name}}
}

func (n *nodeRunbookActionInstance) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Data == nil || w.Data.Context == nil || n.Step == nil {
		return nil
	}
	return w.Data.Context.validateStepAction(n.Step.Name(), n.Action)
}

type nodeExpandRunbookDataSource struct {
	Step       *Step
	Instance   *StepInstance
	DataSource *configs.Resource
}

func (n *nodeExpandRunbookDataSource) Hashcode() interface{} {
	if n.DataSource == nil {
		return nil
	}
	return fmt.Sprintf("%s.data.%s", stepNodePrefix(n.Step, n.Instance), n.DataSource.Addr().String())
}

func (n *nodeExpandRunbookDataSource) Scope() runbookgraph.Scope {
	return stepNodeScope(n.Step, n.Instance)
}

func (n *nodeExpandRunbookDataSource) ReferenceableAddrs() []runbookgraph.ReferenceTarget {
	addr := n.DataSource.Addr()
	return []runbookgraph.ReferenceTarget{runbookaddrs.DataSource{Type: addr.Type, Name: addr.Name}}
}

func (n *nodeExpandRunbookDataSource) References() []runbookgraph.Reference {
	refs := runbookReferencesInExpr(n.Scope(), n.DataSource.Count, n.DataSource.ForEach)
	visitBodyExpressions(n.DataSource.Config, func(expr hcl.Expression) {
		refs = append(refs, runbookReferencesInExpr(n.Scope(), expr)...)
	})
	return refs
}

func (n *nodeExpandRunbookDataSource) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Data == nil || w.Data.Context == nil || n.Step == nil {
		return nil
	}
	return w.Data.Context.validateStepDataSource(n.Step.Name(), n.DataSource)
}

func (n *nodeExpandRunbookDataSource) DynamicExpand(w *RunbookGraphWalker) (*PlanGraph, tfdiags.Diagnostics) {
	return expandStepResourceNode(w, n.Step, n.Instance, n.DataSource, true)
}

type nodeRunbookDataSourceInstance struct {
	Step        *Step
	StepInst    *StepInstance
	DataSource  *configs.Resource
	InstanceKey addrs.InstanceKey
	Repetition  instances.RepetitionData
}

func (n *nodeRunbookDataSourceInstance) Hashcode() interface{} {
	return fmt.Sprintf("%s.data.%s%s", stepNodePrefix(n.Step, n.StepInst), n.DataSource.Addr().String(), instanceKeySuffix(n.InstanceKey))
}

func (n *nodeRunbookDataSourceInstance) Scope() runbookgraph.Scope {
	return stepNodeScope(n.Step, n.StepInst)
}

func (n *nodeRunbookDataSourceInstance) ReferenceableAddrs() []runbookgraph.ReferenceTarget {
	addr := n.DataSource.Addr()
	return []runbookgraph.ReferenceTarget{runbookaddrs.DataSource{Type: addr.Type, Name: addr.Name}}
}

func (n *nodeRunbookDataSourceInstance) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Data == nil || w.Data.Context == nil || n.Step == nil {
		return nil
	}
	return w.Data.Context.validateStepDataSource(n.Step.Name(), n.DataSource)
}

type nodeExpandRunbookList struct {
	Step     *Step
	Instance *StepInstance
	List     *configs.Resource
}

func (n *nodeExpandRunbookList) Hashcode() interface{} {
	if n.List == nil {
		return nil
	}
	return fmt.Sprintf("%s.list.%s", stepNodePrefix(n.Step, n.Instance), n.List.Addr().String())
}

func (n *nodeExpandRunbookList) Scope() runbookgraph.Scope {
	return stepNodeScope(n.Step, n.Instance)
}

func (n *nodeExpandRunbookList) ReferenceableAddrs() []runbookgraph.ReferenceTarget {
	addr := n.List.Addr()
	return []runbookgraph.ReferenceTarget{runbookaddrs.List{Type: addr.Type, Name: addr.Name}}
}

func (n *nodeExpandRunbookList) References() []runbookgraph.Reference {
	refs := runbookReferencesInExpr(n.Scope(), n.List.Count, n.List.ForEach)
	if n.List.List != nil {
		refs = append(refs, runbookReferencesInExpr(n.Scope(), n.List.List.IncludeResource, n.List.List.Limit)...)
	}
	visitBodyExpressions(n.List.Config, func(expr hcl.Expression) {
		refs = append(refs, runbookReferencesInExpr(n.Scope(), expr)...)
	})
	return refs
}

func (n *nodeExpandRunbookList) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Data == nil || w.Data.Context == nil || n.Step == nil {
		return nil
	}
	return w.Data.Context.validateStepList(n.Step.Name(), n.List)
}

func (n *nodeExpandRunbookList) DynamicExpand(w *RunbookGraphWalker) (*PlanGraph, tfdiags.Diagnostics) {
	return expandStepResourceNode(w, n.Step, n.Instance, n.List, false)
}

type nodeRunbookListInstance struct {
	Step        *Step
	StepInst    *StepInstance
	List        *configs.Resource
	InstanceKey addrs.InstanceKey
	Repetition  instances.RepetitionData
}

func (n *nodeRunbookListInstance) Hashcode() interface{} {
	return fmt.Sprintf("%s.list.%s%s", stepNodePrefix(n.Step, n.StepInst), n.List.Addr().String(), instanceKeySuffix(n.InstanceKey))
}

func (n *nodeRunbookListInstance) Scope() runbookgraph.Scope {
	return stepNodeScope(n.Step, n.StepInst)
}

func (n *nodeRunbookListInstance) ReferenceableAddrs() []runbookgraph.ReferenceTarget {
	addr := n.List.Addr()
	return []runbookgraph.ReferenceTarget{runbookaddrs.List{Type: addr.Type, Name: addr.Name}}
}

func (n *nodeRunbookListInstance) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Data == nil || w.Data.Context == nil || n.Step == nil {
		return nil
	}
	return w.Data.Context.validateStepList(n.Step.Name(), n.List)
}

type nodeRunbookStepOutput struct {
	Step     *Step
	Instance *StepInstance
	Output   *configs.Output
}

func (n *nodeRunbookStepOutput) Hashcode() interface{} {
	if n.Output == nil {
		return nil
	}
	return fmt.Sprintf("%s.output.%s", stepNodePrefix(n.Step, n.Instance), n.Output.Name)
}

func (n *nodeRunbookStepOutput) Scope() runbookgraph.Scope {
	return stepNodeScope(n.Step, n.Instance)
}

func (n *nodeRunbookStepOutput) ReferenceableAddrs() []runbookgraph.ReferenceTarget {
	if n.Step == nil || n.Output == nil {
		return nil
	}
	if n.Instance == nil {
		return []runbookgraph.ReferenceTarget{runbookaddrs.ConfigStepOutputValue{Step: runbookaddrs.ConfigStep{Name: n.Step.Name()}, Name: n.Output.Name}}
	}
	return []runbookgraph.ReferenceTarget{runbookaddrs.StepOutputValue{Step: n.Instance.Addr(), Name: n.Output.Name}}
}

func (n *nodeRunbookStepOutput) ReferenceOutside() (runbookgraph.Scope, runbookgraph.Scope) {
	if n.Instance == nil {
		return runbookgraph.RootScope{}, runbookgraph.RootScope{}
	}
	return runbookgraph.RootScope{}, n.Scope()
}

func (n *nodeRunbookStepOutput) References() []runbookgraph.Reference {
	return runbookReferencesInExpr(n.Scope(), n.Output.Expr)
}

func (n *nodeRunbookStepOutput) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Data == nil || w.Data.Context == nil || n.Step == nil {
		return nil
	}
	return w.Data.Context.validateStepOutputValue(n.Step.Name(), n.Output)
}

func addInstanceInternalNodes(g *PlanGraph, stepNode *nodeExpandRunbookStepInstance) {
	if g == nil || g.Graph == nil || stepNode == nil || stepNode.Instance == nil || stepNode.Instance.Step() == nil || stepNode.Instance.Step().Config() == nil {
		return
	}
	step := stepNode.Instance.Step()
	stepCfg := step.Config()
	for _, local := range stepCfg.Locals {
		node := &nodeRunbookStepLocal{Step: step, Instance: stepNode.Instance, Local: local}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for _, action := range stepCfg.Actions {
		node := &nodeExpandRunbookAction{Step: step, Instance: stepNode.Instance, Action: action}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for _, dataSource := range stepCfg.DataSources {
		node := &nodeExpandRunbookDataSource{Step: step, Instance: stepNode.Instance, DataSource: dataSource}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for _, list := range stepCfg.ListResources {
		node := &nodeExpandRunbookList{Step: step, Instance: stepNode.Instance, List: list}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for _, output := range stepCfg.Outputs {
		node := &nodeRunbookStepOutput{Step: step, Instance: stepNode.Instance, Output: output}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
}

func expandStepActionNode(w *RunbookGraphWalker, step *Step, stepInst *StepInstance, action *configs.Action) (*PlanGraph, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if w == nil || w.Operation != runbookgraph.WalkPlan || step == nil || action == nil {
		return nil, diags
	}
	insts, unknown, moreDiags := newRepetitionEvaluator(w.Data.Context).actionInstances(step, action)
	diags = diags.Append(moreDiags)
	if diags.HasErrors() || unknown {
		return nil, diags
	}
	return buildInternalInstanceSubgraph(step, stepInst, insts, func(key addrs.InstanceKey, repetition instances.RepetitionData) dag.Vertex {
		return &nodeRunbookActionInstance{Step: step, StepInst: stepInst, Action: action, InstanceKey: key, Repetition: repetition}
	}), diags
}

func expandStepResourceNode(w *RunbookGraphWalker, step *Step, stepInst *StepInstance, resource *configs.Resource, data bool) (*PlanGraph, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if w == nil || w.Operation != runbookgraph.WalkPlan || step == nil || resource == nil {
		return nil, diags
	}
	insts, unknown, moreDiags := newRepetitionEvaluator(w.Data.Context).resourceInstances(step, resource)
	diags = diags.Append(moreDiags)
	if diags.HasErrors() || unknown {
		return nil, diags
	}
	return buildInternalInstanceSubgraph(step, stepInst, insts, func(key addrs.InstanceKey, repetition instances.RepetitionData) dag.Vertex {
		if data {
			return &nodeRunbookDataSourceInstance{Step: step, StepInst: stepInst, DataSource: resource, InstanceKey: key, Repetition: repetition}
		}
		return &nodeRunbookListInstance{Step: step, StepInst: stepInst, List: resource, InstanceKey: key, Repetition: repetition}
	}), diags
}

func buildInternalInstanceSubgraph(step *Step, stepInst *StepInstance, insts map[addrs.InstanceKey]instances.RepetitionData, makeNode func(addrs.InstanceKey, instances.RepetitionData) dag.Vertex) *PlanGraph {
	if len(insts) == 0 {
		return nil
	}
	g := &PlanGraph{
		Graph:           &dag.AcyclicGraph{},
		ConfigSteps:     map[string]*nodeExpandRunbookStep{},
		StepVertices:    map[string]*nodeExpandRunbookStepInstance{},
		InstancesByStep: map[string]map[addrs.InstanceKey]*StepInstance{},
	}
	g.Root = &nodeRunbookRoot{}
	g.Graph.Add(g.Root)
	keys := make([]string, 0, len(insts))
	byKey := make(map[string]addrs.InstanceKey, len(insts))
	for key := range insts {
		keyStr := ""
		if key != addrs.NoKey && key != nil {
			keyStr = key.String()
		}
		keys = append(keys, keyStr)
		byKey[keyStr] = key
	}
	sort.Strings(keys)
	for _, keyStr := range keys {
		key := byKey[keyStr]
		node := makeNode(key, insts[key])
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, g.Root))
	}
	return g
}

func addInstanceBehaviorNodes(g *PlanGraph, stepNode *nodeExpandRunbookStepInstance) {
	if g == nil || g.Graph == nil || stepNode == nil || stepNode.Instance == nil || stepNode.Instance.Step() == nil || stepNode.Instance.Step().Config() == nil {
		return
	}
	step := stepNode.Instance.Step()
	stepCfg := step.Config()
	for idx, execution := range stepCfg.Executions {
		node := &nodeRunbookExecute{Step: step, Instance: stepNode.Instance, Execution: execution, Index: idx}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for idx, condition := range stepCfg.Preconditions {
		node := &nodeRunbookCondition{Step: step, Instance: stepNode.Instance, Condition: condition, Index: idx}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for idx, condition := range stepCfg.Postconditions {
		node := &nodeRunbookCondition{Step: step, Instance: stepNode.Instance, Condition: condition, Index: len(stepCfg.Preconditions) + idx}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
}

func addConfigBehaviorNodes(g *PlanGraph, stepNode *nodeExpandRunbookStep, step *Step) {
	if g == nil || g.Graph == nil || stepNode == nil || step == nil || step.Config() == nil {
		return
	}
	stepCfg := step.Config()
	for idx, execution := range stepCfg.Executions {
		node := &nodeRunbookExecute{Step: step, Execution: execution, Index: idx}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for idx, condition := range stepCfg.Preconditions {
		node := &nodeRunbookCondition{Step: step, Condition: condition, Index: idx}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
	for idx, condition := range stepCfg.Postconditions {
		node := &nodeRunbookCondition{Step: step, Condition: condition, Index: len(stepCfg.Preconditions) + idx}
		g.Graph.Add(node)
		g.Graph.Connect(dag.BasicEdge(node, stepNode))
	}
}

func stepNodeScope(step *Step, inst *StepInstance) runbookgraph.Scope {
	if inst != nil {
		return runbookgraph.StepInstanceScope{StepAddress: inst.Addr().String()}
	}
	if step != nil {
		return runbookgraph.StepConfigScope{StepName: step.Name()}
	}
	return runbookgraph.RootScope{}
}

func stepNodePrefix(step *Step, inst *StepInstance) string {
	if inst != nil {
		return inst.Addr().String()
	}
	if step != nil {
		return step.Addr().String()
	}
	return "step"
}

func instanceKeySuffix(key addrs.InstanceKey) string {
	if key == nil || key == addrs.NoKey {
		return ""
	}
	return key.String()
}
