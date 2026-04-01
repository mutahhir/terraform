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

type runbookGraphNode interface {
	dag.Vertex
	runbookGraphNodeExecutable
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

type nodeRunbookRoot struct{}

func (n *nodeRunbookRoot) Hashcode() interface{} { return "runbook.root" }
func (n *nodeRunbookRoot) Name() string          { return "runbook.root" }
func (n *nodeRunbookRoot) ExecuteRunbook(*RunbookGraphWalker) tfdiags.Diagnostics {
	return nil
}

type nodeRootVariable struct {
	NameValue string
	Variable  *configs.Variable
}

func (n *nodeRootVariable) Hashcode() interface{} { return "runbook.var." + n.NameValue }
func (n *nodeRootVariable) Name() string          { return "var." + n.NameValue }
func (n *nodeRootVariable) ExecuteRunbook(*RunbookGraphWalker) tfdiags.Diagnostics {
	return nil
}

type nodeRunbookOutput struct {
	NameValue string
}

func (n *nodeRunbookOutput) Hashcode() interface{} { return "runbook.output." + n.NameValue }
func (n *nodeRunbookOutput) Name() string          { return "output." + n.NameValue }
func (n *nodeRunbookOutput) ExecuteRunbook(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Context == nil {
		return nil
	}
	return w.Context.validateRunbookOutput(n.NameValue)
}

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

func (n *nodeExpandRunbookStep) ExecuteRunbook(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Context == nil {
		return nil
	}
	return w.Context.validateStepShell(n.Step)
}

func (n *nodeExpandRunbookStep) DynamicExpand(w *RunbookGraphWalker) (*PlanGraph, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if w == nil || w.Operation != walkPlan || n.Step == nil {
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
		vertex := &nodeExpandRunbookStepInstance{Instance: byAddr[addr]}
		subgraph.Graph.Add(vertex)
		subgraph.Graph.Connect(dag.BasicEdge(subgraph.Root, vertex))
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

func (n *nodeExpandRunbookStepInstance) ExecuteRunbook(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.RootGraph == nil || n.Instance == nil {
		return nil
	}
	w.RootGraph.StepVertices[n.Instance.Addr().String()] = n
	return nil
}

type nodeRunbookStepLocal struct {
	Step  *Step
	Local *configs.Local
}

func (n *nodeRunbookStepLocal) Hashcode() interface{} {
	if n.Step == nil || n.Local == nil {
		return nil
	}
	return "runbook.step." + n.Step.Name() + ".local." + n.Local.Name
}

func (n *nodeRunbookStepLocal) ExecuteRunbook(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Context == nil || n.Step == nil {
		return nil
	}
	return w.Context.validateStepLocal(n.Step.Name(), n.Local)
}

type nodeRunbookAction struct {
	Step   *Step
	Action *configs.Action
}

func (n *nodeRunbookAction) Hashcode() interface{} {
	if n.Step == nil || n.Action == nil {
		return nil
	}
	return "runbook.step." + n.Step.Name() + ".action." + n.Action.Addr().String()
}

func (n *nodeRunbookAction) ExecuteRunbook(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Context == nil || n.Step == nil {
		return nil
	}
	return w.Context.validateStepAction(n.Step.Name(), n.Action)
}

type nodeRunbookDataSource struct {
	Step       *Step
	DataSource *configs.Resource
}

func (n *nodeRunbookDataSource) Hashcode() interface{} {
	if n.Step == nil || n.DataSource == nil {
		return nil
	}
	return "runbook.step." + n.Step.Name() + ".data." + n.DataSource.Addr().String()
}

func (n *nodeRunbookDataSource) ExecuteRunbook(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Context == nil || n.Step == nil {
		return nil
	}
	return w.Context.validateStepDataSource(n.Step.Name(), n.DataSource)
}

type nodeRunbookList struct {
	Step *Step
	List *configs.Resource
}

func (n *nodeRunbookList) Hashcode() interface{} {
	if n.Step == nil || n.List == nil {
		return nil
	}
	return "runbook.step." + n.Step.Name() + ".list." + n.List.Addr().String()
}

func (n *nodeRunbookList) ExecuteRunbook(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Context == nil || n.Step == nil {
		return nil
	}
	return w.Context.validateStepList(n.Step.Name(), n.List)
}

type nodeRunbookStepOutput struct {
	Step   *Step
	Output *configs.Output
}

func (n *nodeRunbookStepOutput) Hashcode() interface{} {
	if n.Step == nil || n.Output == nil {
		return nil
	}
	return "runbook.step." + n.Step.Name() + ".output." + n.Output.Name
}

func (n *nodeRunbookStepOutput) ExecuteRunbook(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Context == nil || n.Step == nil {
		return nil
	}
	return w.Context.validateStepOutputValue(n.Step.Name(), n.Output)
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
