// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/runbooks/runbookgraph"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type nodeExpandRunbookStep struct {
	Step *Step
}

func (n *nodeExpandRunbookStep) Hashcode() interface{} {
	if n.Step == nil {
		return nil
	}
	return "runbook.step." + n.Step.Name()
}

func (n *nodeExpandRunbookStep) Name() string {
	if n.Step == nil {
		return ""
	}
	return n.Step.Name()
}

func (n *nodeExpandRunbookStep) Scope() runbookgraph.Scope {
	return runbookgraph.RootScope{}
}

func (n *nodeExpandRunbookStep) References() []runbookgraph.Reference {
	if n == nil || n.Step == nil || n.Step.Config() == nil {
		return nil
	}
	stepCfg := n.Step.Config()
	refs := runbookReferencesInExpr(runbookgraph.RootScope{}, stepCfg.Count, stepCfg.ForEach)
	for _, execution := range stepCfg.Executions {
		for _, action := range execution.InvokeAction {
			if target, ok := action.(runbookgraph.ReferenceTarget); ok {
				refs = append(refs, runbookgraph.Reference{Target: target})
			}
		}
	}
	for _, condition := range stepCfg.Preconditions {
		refs = append(refs, runbookReferencesInExpr(runbookgraph.RootScope{}, condition.Condition, condition.ErrorMessage)...)
	}
	for _, condition := range stepCfg.Postconditions {
		refs = append(refs, runbookReferencesInExpr(runbookgraph.RootScope{}, condition.Condition, condition.ErrorMessage)...)
	}
	return refs
}

func (n *nodeExpandRunbookStep) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Data == nil || w.Data.Context == nil {
		return nil
	}
	return w.Data.Context.validateStepShell(n.Step)
}

func (n *nodeExpandRunbookStep) DynamicExpand(w *RunbookGraphWalker) (*PlanGraph, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if w == nil || w.Operation != runbookgraph.WalkPlan || n.Step == nil {
		return nil, diags
	}

	instances, unknown, moreDiags := n.Step.CheckInstances()
	diags = diags.Append(moreDiags)
	if diags.HasErrors() {
		return nil, diags
	}
	if unknown {
		var subject *hcl.Range
		if cfg := n.Step.Config(); cfg != nil {
			switch {
			case cfg.Count != nil:
				subject = cfg.Count.Range().Ptr()
			case cfg.ForEach != nil:
				subject = cfg.ForEach.Range().Ptr()
			}
		}
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Unknown step repetition during planning",
			Detail:   fmt.Sprintf("The repetition for step %q is not fully known at plan time. Runbooks require all step instances to be determined before execution planning.", n.Step.Name()),
			Subject:  subject,
		})
		return nil, diags
	}

	if w.RootGraph != nil {
		w.RootGraph.InstancesByStep[n.Step.Name()] = instances
	}

	subgraph := &PlanGraph{
		Config:          w.Graph.Config,
		Operation:       w.Operation,
		Graph:           &dag.AcyclicGraph{},
		ConfigSteps:     map[string]*nodeExpandRunbookStep{},
		StepVertices:    map[string]*nodeExpandRunbookStepInstance{},
		InstancesByStep: map[string]map[addrs.InstanceKey]*StepInstance{},
	}
	subgraph.Root = &nodeRunbookRoot{}
	subgraph.Graph.Add(subgraph.Root)

	keys := make([]string, 0, len(instances))
	byAddr := make(map[string]*StepInstance, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		addr := inst.Addr().String()
		keys = append(keys, addr)
		byAddr[addr] = inst
	}
	sort.Strings(keys)
	for _, addr := range keys {
		instNode := &nodeExpandRunbookStepInstance{Instance: byAddr[addr]}
		subgraph.Graph.Add(instNode)
		subgraph.Graph.Connect(dag.BasicEdge(instNode, subgraph.Root))
		addInstanceInternalNodes(subgraph, instNode)
		addInstanceBehaviorNodes(subgraph, instNode)
	}

	if err := (&runbookgraph.ReferenceTransformer[*PlanGraph]{}).Transform(subgraph); err != nil {
		return nil, diags.Append(err)
	}
	if err := (&runbookgraph.TransitiveReductionTransformer[*PlanGraph]{}).Transform(subgraph); err != nil {
		return nil, diags.Append(err)
	}

	return subgraph, diags
}

type nodeExpandRunbookStepInstance struct {
	Instance *StepInstance
}

func (n *nodeExpandRunbookStepInstance) Hashcode() interface{} {
	if n.Instance == nil {
		return nil
	}
	return n.Instance.Addr().String()
}

func (n *nodeExpandRunbookStepInstance) Name() string {
	if n.Instance == nil {
		return ""
	}
	return n.Instance.Addr().String()
}

func (n *nodeExpandRunbookStepInstance) Scope() runbookgraph.Scope {
	if n == nil || n.Instance == nil {
		return runbookgraph.StepInstanceScope{}
	}
	return runbookgraph.StepInstanceScope{StepAddress: n.Instance.Addr().String()}
}

func (n *nodeExpandRunbookStepInstance) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.RootGraph == nil || n.Instance == nil {
		return nil
	}
	w.RootGraph.StepVertices[n.Instance.Addr().String()] = n
	return nil
}

type nodeRunbookExecute struct {
	Step      *Step
	Instance  *StepInstance
	Execution *runbookconfig.Execution
	Index     int
}

func (n *nodeRunbookExecute) Hashcode() interface{} {
	return fmt.Sprintf("%s.execute.%d", stepNodePrefix(n.Step, n.Instance), n.Index)
}

func (n *nodeRunbookExecute) Scope() runbookgraph.Scope {
	return stepNodeScope(n.Step, n.Instance)
}

func (n *nodeRunbookExecute) References() []runbookgraph.Reference {
	if n == nil || n.Execution == nil {
		return nil
	}
	refs := make([]runbookgraph.Reference, 0, len(n.Execution.InvokeAction))
	for _, action := range n.Execution.InvokeAction {
		if target, ok := action.(runbookgraph.ReferenceTarget); ok {
			refs = append(refs, runbookgraph.Reference{Target: target})
		}
	}
	return refs
}

func (n *nodeRunbookExecute) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if w == nil || w.Data == nil || w.Data.Context == nil || n.Execution == nil || n.Step == nil {
		return diags
	}
	for _, action := range n.Execution.InvokeAction {
		diags = diags.Append(w.Data.Context.validateExecutableAction(n.Step.Name(), action))
	}
	return diags
}

type nodeRunbookCondition struct {
	Step      *Step
	Instance  *StepInstance
	Condition *runbookconfig.Condition
	Index     int
}

func (n *nodeRunbookCondition) Hashcode() interface{} {
	return fmt.Sprintf("%s.condition.%s.%d", stepNodePrefix(n.Step, n.Instance), n.Condition.Kind, n.Index)
}

func (n *nodeRunbookCondition) Scope() runbookgraph.Scope {
	return stepNodeScope(n.Step, n.Instance)
}

func (n *nodeRunbookCondition) References() []runbookgraph.Reference {
	if n == nil || n.Condition == nil {
		return nil
	}
	return runbookReferencesInExpr(n.Scope(), n.Condition.Condition, n.Condition.ErrorMessage)
}

func (n *nodeRunbookCondition) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if w == nil || w.Data == nil || w.Data.Context == nil || n.Condition == nil || n.Step == nil {
		return diags
	}
	return diags.Append(w.Data.Context.validateScopedExpressions(n.Step.Name(), repetitionValidationScope{}, n.Condition.Condition, n.Condition.ErrorMessage))
}
