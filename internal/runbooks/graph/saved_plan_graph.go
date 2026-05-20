package runbookgraph

import (
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/dag"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/terraform"
)

func buildExpandedStepInstanceGraph(stepName string, cfg *runbookconfigs.Step, expanded expandedStepInstance) (*terraform.Graph, error) {
	var g terraform.Graph
	instance := &NodeStepInstance{
		StepName:       stepName,
		InstanceKey:    expanded.key,
		RepetitionData: expanded.repetitionData,
		Config:         cfg,
		Runtime:        expanded.runtime,
	}
	g.Add(instance)
	if cfg == nil {
		return &g, nil
	}
	refTargets := map[string]dag.Vertex{}
	locals := make([]*NodeStepLocal, 0, len(cfg.Locals))
	actions := make([]*NodeStepAction, 0, len(cfg.Actions))
	dataNodes := make([]*NodeStepData, 0, len(cfg.DataSources))
	listNodes := make([]*NodeStepList, 0, len(cfg.ListResources))
	for _, action := range cfg.Actions {
		child := &NodeStepAction{Step: instance, Action: action}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		refTargets[terraformaddrs.Action{Type: action.Type, Name: action.Name}.String()] = child
		actions = append(actions, child)
	}
	for _, data := range cfg.DataSources {
		child := &NodeStepData{Step: instance, Data: data, RefreshAtApply: isDynamicDataSource(cfg, data)}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		refTargets[terraformaddrs.Resource{Mode: terraformaddrs.DataResourceMode, Type: data.Type, Name: data.Name}.String()] = child
		dataNodes = append(dataNodes, child)
	}
	for _, list := range cfg.ListResources {
		child := &NodeStepList{Step: instance, List: list}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		refTargets[terraformaddrs.Resource{Mode: terraformaddrs.ListResourceMode, Type: list.Type, Name: list.Name}.String()] = child
		listNodes = append(listNodes, child)
	}
	for _, local := range cfg.Locals {
		child := &NodeStepLocal{Step: instance, Local: local}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		refTargets[terraformaddrs.LocalValue{Name: local.Name}.String()] = child
		locals = append(locals, child)
	}
	executions := make([]*NodeStepExecution, 0, len(cfg.Executions))
	for index, execution := range cfg.Executions {
		child := &NodeStepExecution{Step: instance, Index: index, Execution: execution}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		executions = append(executions, child)
	}
	preconditions := make([]*NodeStepCondition, 0, len(cfg.Preconditions))
	for _, condition := range cfg.Preconditions {
		child := &NodeStepCondition{Step: instance, Condition: condition}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		preconditions = append(preconditions, child)
	}
	postconditions := make([]*NodeStepCondition, 0, len(cfg.Postconditions))
	for _, condition := range cfg.Postconditions {
		child := &NodeStepCondition{Step: instance, Condition: condition}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		postconditions = append(postconditions, child)
	}
	outputs := make([]*NodeStepOutput, 0, len(cfg.Outputs))
	for _, output := range cfg.Outputs {
		child := &NodeStepOutput{Step: instance, Output: output}
		g.Add(child)
		g.Connect(dag.BasicEdge(instance, child))
		refTargets[runbookaddrs.StepOutput{Step: runbookaddrs.StepInstance{StepName: stepName, InstanceKey: expanded.key}, OutputName: output.Name}.String()] = child
		outputs = append(outputs, child)
	}
	finalize := &NodeStepFinalize{Step: instance}
	g.Add(finalize)
	g.Connect(dag.BasicEdge(finalize, instance))
	for _, local := range locals {
		connectStepReferences(&g, stepName, local, referencesForStepLocal(local.Local), refTargets)
	}
	for _, action := range actions {
		connectStepReferences(&g, stepName, action, referencesForStepAction(action.Action), refTargets)
	}
	for _, data := range dataNodes {
		connectStepReferences(&g, stepName, data, referencesForStepResource(data.Data), refTargets)
	}
	for _, list := range listNodes {
		connectStepReferences(&g, stepName, list, referencesForStepResource(list.List), refTargets)
	}
	for _, execution := range executions {
		connectStepReferences(&g, stepName, execution, referencesForStepExecution(execution.Execution), refTargets)
	}
	for _, condition := range preconditions {
		connectStepReferences(&g, stepName, condition, referencesForStepCondition(condition.Condition), refTargets)
	}
	for _, condition := range postconditions {
		connectStepReferences(&g, stepName, condition, referencesForStepCondition(condition.Condition), refTargets)
		for _, execution := range executions {
			g.Connect(dag.BasicEdge(condition, execution))
		}
	}
	for _, output := range outputs {
		connectStepReferences(&g, stepName, output, referencesForStepOutput(output.Output), refTargets)
		if outputReferencesDynamicData(output.Output, cfg) {
			for _, execution := range executions {
				g.Connect(dag.BasicEdge(output, execution))
			}
		}
	}
	for _, local := range locals {
		g.Connect(dag.BasicEdge(finalize, local))
	}
	for _, action := range actions {
		g.Connect(dag.BasicEdge(finalize, action))
	}
	for _, data := range dataNodes {
		g.Connect(dag.BasicEdge(finalize, data))
	}
	for _, list := range listNodes {
		g.Connect(dag.BasicEdge(finalize, list))
	}
	for _, execution := range executions {
		g.Connect(dag.BasicEdge(finalize, execution))
	}
	for _, condition := range preconditions {
		g.Connect(dag.BasicEdge(finalize, condition))
	}
	for _, condition := range postconditions {
		g.Connect(dag.BasicEdge(finalize, condition))
	}
	for _, output := range outputs {
		g.Connect(dag.BasicEdge(finalize, output))
	}
	return &g, nil
}
