// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"fmt"
	"sort"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type runbookGraphNodeExecutable interface {
	ExecuteRunbook(*RunbookGraphWalker) tfdiags.Diagnostics
}

type runbookGraphNodeDynamicExpandable interface {
	DynamicExpand(*RunbookGraphWalker) (*PlanGraph, tfdiags.Diagnostics)
}

type RunbookGraphWalker struct {
	Context   *RunbookContext
	Graph     *PlanGraph
	RootGraph *PlanGraph
	Operation walkOperation

	mu *sync.Mutex
}

func newRunbookGraphWalker(ctx *RunbookContext, graph *PlanGraph, operation walkOperation) *RunbookGraphWalker {
	return &RunbookGraphWalker{
		Context:   ctx,
		Graph:     graph,
		RootGraph: graph,
		Operation: operation,
		mu:        &sync.Mutex{},
	}
}

func (w *RunbookGraphWalker) child(graph *PlanGraph) *RunbookGraphWalker {
	if w == nil {
		return nil
	}
	return &RunbookGraphWalker{
		Context:   w.Context,
		Graph:     graph,
		RootGraph: w.RootGraph,
		Operation: w.Operation,
		mu:        w.mu,
	}
}

func (w *RunbookGraphWalker) execute(node runbookGraphNodeExecutable) tfdiags.Diagnostics {
	if w == nil || node == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return node.ExecuteRunbook(w)
}

func (w *RunbookGraphWalker) expand(node runbookGraphNodeDynamicExpandable) (*PlanGraph, tfdiags.Diagnostics) {
	if w == nil || node == nil {
		return nil, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return node.DynamicExpand(w)
}

type runbookRootVertex struct{}

func (v runbookRootVertex) Hashcode() interface{} { return "runbook.root" }
func (v runbookRootVertex) Name() string          { return "runbook.root" }
func (v runbookRootVertex) ExecuteRunbook(*RunbookGraphWalker) tfdiags.Diagnostics {
	return nil
}

type runbookVariableVertex struct {
	NameValue string
	Variable  *configs.Variable
}

func (v runbookVariableVertex) Hashcode() interface{} { return "runbook.var." + v.NameValue }
func (v runbookVariableVertex) Name() string          { return "var." + v.NameValue }
func (v runbookVariableVertex) ExecuteRunbook(*RunbookGraphWalker) tfdiags.Diagnostics {
	return nil
}

type runbookOutputVertex struct {
	NameValue string
}

func (v runbookOutputVertex) Hashcode() interface{} { return "runbook.output." + v.NameValue }
func (v runbookOutputVertex) Name() string          { return "output." + v.NameValue }
func (v runbookOutputVertex) ExecuteRunbook(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Context == nil {
		return nil
	}
	return w.Context.validateRunbookOutput(v.NameValue)
}

type runbookStepVertex struct {
	Step *Step
}

func (v runbookStepVertex) Hashcode() interface{} {
	if v.Step == nil {
		return nil
	}
	return "runbook.step." + v.Step.Name()
}

func (v runbookStepVertex) Name() string {
	if v.Step == nil {
		return ""
	}
	return v.Step.Name()
}

func (v runbookStepVertex) ExecuteRunbook(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Context == nil {
		return nil
	}
	return w.Context.validateStep(v.Step)
}

func (v runbookStepVertex) DynamicExpand(w *RunbookGraphWalker) (*PlanGraph, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if w == nil || w.Operation != walkPlan || v.Step == nil {
		return nil, diags
	}

	instances, unknown, moreDiags := v.Step.CheckInstances()
	diags = diags.Append(moreDiags)
	if diags.HasErrors() {
		return nil, diags
	}
	if unknown {
		var subject *hcl.Range
		if cfg := v.Step.Config(); cfg != nil {
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
			Detail:   fmt.Sprintf("The repetition for step %q is not fully known at plan time. Runbooks require all step instances to be determined before execution planning.", v.Step.Name()),
			Subject:  subject,
		})
		return nil, diags
	}

	if w.RootGraph != nil {
		w.RootGraph.InstancesByStep[v.Step.Name()] = instances
	}

	subgraph := &PlanGraph{
		Config:          w.Graph.Config,
		Operation:       w.Operation,
		Graph:           &dag.AcyclicGraph{},
		ConfigSteps:     map[string]runbookStepVertex{},
		StepVertices:    map[string]planStepVertex{},
		InstancesByStep: map[string]map[addrs.InstanceKey]*StepInstance{},
	}
	subgraph.Root = runbookRootVertex{}
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
		vertex := planStepVertex{Instance: byAddr[addr]}
		subgraph.Graph.Add(vertex)
		subgraph.Graph.Connect(dag.BasicEdge(subgraph.Root, vertex))
	}

	return subgraph, diags
}

func (v planStepVertex) ExecuteRunbook(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.RootGraph == nil || v.Instance == nil {
		return nil
	}
	w.RootGraph.StepVertices[v.Instance.Addr().String()] = v
	return nil
}

func (c *RunbookContext) resetGraphBuildState() {
	if c == nil {
		return
	}
	c.usedWorkspaceOutputNames = c.usedWorkspaceOutputNames[:0]
	for stepName := range c.workspaceOutputsByStep {
		c.workspaceOutputsByStep[stepName] = c.workspaceOutputsByStep[stepName][:0]
	}
	c.stepDependencyGraph = &dag.AcyclicGraph{}
	for _, v := range c.stepVertices {
		c.stepDependencyGraph.Add(v)
	}
}

func visitBodyExpressions(body hcl.Body, visit func(hcl.Expression)) {
	if body == nil || visit == nil {
		return
	}

	syntaxBody, ok := body.(*hclsyntax.Body)
	if !ok {
		return
	}

	for _, attr := range syntaxBody.Attributes {
		visit(attr.Expr)
	}
	for _, block := range syntaxBody.Blocks {
		visitBodyExpressions(block.Body, visit)
	}
}
