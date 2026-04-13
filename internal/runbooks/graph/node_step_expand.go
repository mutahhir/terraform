package runbookgraph

import (
	"fmt"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/dag"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runtime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type NodeExpandStep struct {
	// NodeExpandStep represents a declared step in runbook config before runtime
	// expansion has produced concrete step instances.
	StepName string
	Config   *runbookconfigs.Step
	Runtime  *runtime.Step
}

func (n *NodeExpandStep) Hashcode() interface{} {
	return [2]string{"step_expand", n.StepName}
}

func (n *NodeExpandStep) Name() string {
	return fmt.Sprintf("step.%s (expand)", n.StepName)
}

func (n *NodeExpandStep) DynamicExpand(ctx *EvalContext) (*terraform.Graph, tfdiags.Diagnostics) {
	var g terraform.Graph
	runtimeStep := n.Runtime
	if ctx != nil {
		runtimeStep = ctx.EnsureStep(n.StepName, n.Config, n.Runtime)
	}
	instance := &NodeStepInstance{
		StepName: n.StepName,
		Config:   n.Config,
		Runtime:  runtimeStep,
	}
	g.Add(instance)
	refTargets := map[string]dag.Vertex{}
	locals := make([]*NodeStepLocal, 0, len(n.Config.Locals))
	actions := make([]*NodeStepAction, 0, len(n.Config.Actions))
	dataNodes := make([]*NodeStepData, 0, len(n.Config.DataSources))
	listNodes := make([]*NodeStepList, 0, len(n.Config.ListResources))
	for _, action := range n.Config.Actions {
		child := &NodeStepAction{StepName: n.StepName, Action: action}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		refTargets[terraformaddrs.Action{Type: action.Type, Name: action.Name}.String()] = child
		actions = append(actions, child)
	}
	for _, data := range n.Config.DataSources {
		child := &NodeStepData{StepName: n.StepName, Data: data}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		refTargets[terraformaddrs.Resource{Mode: terraformaddrs.DataResourceMode, Type: data.Type, Name: data.Name}.String()] = child
		dataNodes = append(dataNodes, child)
	}
	for _, list := range n.Config.ListResources {
		child := &NodeStepList{StepName: n.StepName, List: list}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		refTargets[terraformaddrs.Resource{Mode: terraformaddrs.ListResourceMode, Type: list.Type, Name: list.Name}.String()] = child
		listNodes = append(listNodes, child)
	}
	for _, local := range n.Config.Locals {
		child := &NodeStepLocal{StepName: n.StepName, Local: local}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		refTargets[terraformaddrs.LocalValue{Name: local.Name}.String()] = child
		locals = append(locals, child)
	}
	executions := make([]*NodeStepExecution, 0, len(n.Config.Executions))
	for index, execution := range n.Config.Executions {
		child := &NodeStepExecution{StepName: n.StepName, Index: index, Execution: execution}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		executions = append(executions, child)
	}
	conditions := make([]*NodeStepCondition, 0, len(n.Config.Preconditions)+len(n.Config.Postconditions))
	for _, condition := range n.Config.Preconditions {
		child := &NodeStepCondition{StepName: n.StepName, Condition: condition}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		conditions = append(conditions, child)
	}
	for _, condition := range n.Config.Postconditions {
		child := &NodeStepCondition{StepName: n.StepName, Condition: condition}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		conditions = append(conditions, child)
	}
	outputs := make([]*NodeStepOutput, 0, len(n.Config.Outputs))
	for _, output := range n.Config.Outputs {
		child := &NodeStepOutput{StepName: n.StepName, Output: output}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		refTargets[runbookaddrs.StepOutput{StepName: n.StepName, OutputName: output.Name}.String()] = child
		outputs = append(outputs, child)
	}
	for _, local := range locals {
		connectStepReferences(&g, n.StepName, local, referencesForStepLocal(local.Local), refTargets)
	}
	for _, action := range actions {
		connectStepReferences(&g, n.StepName, action, referencesForStepAction(action.Action), refTargets)
	}
	for _, data := range dataNodes {
		connectStepReferences(&g, n.StepName, data, referencesForStepResource(data.Data), refTargets)
	}
	for _, list := range listNodes {
		connectStepReferences(&g, n.StepName, list, referencesForStepResource(list.List), refTargets)
	}
	for _, execution := range executions {
		connectStepReferences(&g, n.StepName, execution, referencesForStepExecution(execution.Execution), refTargets)
	}
	for _, condition := range conditions {
		connectStepReferences(&g, n.StepName, condition, referencesForStepCondition(condition.Condition), refTargets)
	}
	for _, output := range outputs {
		connectStepReferences(&g, n.StepName, output, referencesForStepOutput(output.Output), refTargets)
	}
	if err := (&terraform.RootTransformer{}).Transform(&g); err != nil {
		return nil, tfdiags.Diagnostics{}.Append(err)
	}
	return &g, nil
}

func connectStepReferences(g *terraform.Graph, currentStep string, from dag.Vertex, refs []runbookaddrs.Referenceable, targets map[string]dag.Vertex) {
	for _, ref := range refs {
		key := ref.String()
		if stepOutput, ok := ref.(runbookaddrs.StepOutput); ok && stepOutput.StepName == "" {
			key = runbookaddrs.StepOutput{StepName: currentStep, OutputName: stepOutput.OutputName}.String()
		}
		dep, ok := targets[key]
		if !ok {
			continue
		}
		g.Connect(dag.BasicEdge(from, dep))
	}
}

var (
	_ dag.Vertex                 = (*NodeExpandStep)(nil)
	_ GraphNodeDynamicExpandable = (*NodeExpandStep)(nil)
)
