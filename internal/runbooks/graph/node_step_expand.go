package runbookgraph

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/dag"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runtime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

type NodeExpandStep struct {
	// NodeExpandStep represents a declared step in runbook config before runtime
	// expansion has produced concrete step instances.
	StepName string
	Config   *runbookconfigs.Step
	Runtime  *runtime.Step
}

type expandedStepInstance struct {
	key            terraformaddrs.InstanceKey
	repetitionData *terraform.InstanceKeyEvalData
	runtime        *runtime.Step
}

func (n *NodeExpandStep) Hashcode() interface{} {
	return [2]string{"step_expand", n.StepName}
}

func (n *NodeExpandStep) Name() string {
	return fmt.Sprintf("step.%s (expand)", n.StepName)
}

func cloneRuntimeStep(existing *runtime.Step, config *runbookconfigs.Step, name string, key terraformaddrs.InstanceKey, index int) *runtime.Step {
	if existing == nil {
		return &runtime.Step{Name: name, Config: config, Index: index, InstanceKey: key}
	}
	copy := *existing
	copy.Name = name
	copy.Config = config
	copy.Index = index
	copy.InstanceKey = key
	return &copy
}

func applyRepetitionData(step *runtime.Step, repetitionData *terraform.InstanceKeyEvalData) {
	if step == nil {
		return
	}
	if repetitionData == nil {
		step.RepetitionData = nil
		return
	}
	copy := *repetitionData
	step.RepetitionData = &copy
}

func evalRunbookForEach(expr hcl.Expression, ctx *EvalContext, stepName string) (map[string]cty.Value, tfdiags.Diagnostics) {
	value, diags := ctx.EvaluateExpr(stepName, expr)
	if diags.HasErrors() {
		return nil, diags
	}
	if value.IsNull() {
		return map[string]cty.Value{}, nil
	}
	if !value.IsKnown() || !value.CanIterateElements() {
		return nil, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid for_each argument",
			Detail:   "The for_each argument must evaluate to a known collection.",
			Subject:  expr.Range().Ptr(),
		})
	}
	return value.AsValueMap(), nil
}

func evalRunbookCount(expr hcl.Expression, ctx *EvalContext, stepName string) (int, tfdiags.Diagnostics) {
	value, diags := ctx.EvaluateExpr(stepName, expr)
	if diags.HasErrors() {
		return 0, diags
	}
	if value.IsNull() || !value.IsKnown() || value.Type() != cty.Number {
		return 0, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid count argument",
			Detail:   "The count argument must evaluate to a known number.",
			Subject:  expr.Range().Ptr(),
		})
	}
	count, _ := value.AsBigFloat().Int64()
	return int(count), diags
}

func (n *NodeExpandStep) expandInstances(ctx *EvalContext) ([]expandedStepInstance, tfdiags.Diagnostics) {
	if n.Config == nil {
		runtimeStep := cloneRuntimeStep(n.Runtime, nil, n.StepName, terraformaddrs.NoKey, 0)
		applyRepetitionData(runtimeStep, nil)
		return []expandedStepInstance{{key: terraformaddrs.NoKey, runtime: runtimeStep}}, nil
	}
	if n.Config.Count != nil {
		count, diags := evalRunbookCount(n.Config.Count, ctx, n.StepName)
		if diags.HasErrors() {
			return nil, diags
		}
		ret := make([]expandedStepInstance, 0, count)
		for i := 0; i < count; i++ {
			key := terraformaddrs.IntKey(i)
			data := terraform.InstanceKeyEvalData{CountIndex: cty.NumberIntVal(int64(i))}
			runtimeStep := cloneRuntimeStep(n.Runtime, n.Config, n.StepName, key, i)
			applyRepetitionData(runtimeStep, &data)
			ret = append(ret, expandedStepInstance{key: key, repetitionData: &data, runtime: runtimeStep})
		}
		return ret, nil
	}
	if n.Config.ForEach != nil {
		forEach, diags := evalRunbookForEach(n.Config.ForEach, ctx, n.StepName)
		if diags.HasErrors() {
			return nil, diags
		}
		keys := make([]string, 0, len(forEach))
		for key := range forEach {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		ret := make([]expandedStepInstance, 0, len(keys))
		for index, keyStr := range keys {
			key := terraformaddrs.StringKey(keyStr)
			data := terraform.InstanceKeyEvalData{EachKey: cty.StringVal(keyStr), EachValue: forEach[keyStr]}
			runtimeStep := cloneRuntimeStep(n.Runtime, n.Config, n.StepName, key, index)
			applyRepetitionData(runtimeStep, &data)
			ret = append(ret, expandedStepInstance{key: key, repetitionData: &data, runtime: runtimeStep})
		}
		return ret, nil
	}
	runtimeStep := cloneRuntimeStep(n.Runtime, n.Config, n.StepName, terraformaddrs.NoKey, 0)
	applyRepetitionData(runtimeStep, nil)
	return []expandedStepInstance{{key: terraformaddrs.NoKey, runtime: runtimeStep}}, nil
}

func (n *NodeExpandStep) DynamicExpand(ctx *EvalContext) (*terraform.Graph, tfdiags.Diagnostics) {
	var g terraform.Graph
	instances, diags := n.expandInstances(ctx)
	if diags.HasErrors() {
		return nil, diags
	}
	for _, expanded := range instances {
		instance := &NodeStepInstance{
			StepName:       n.StepName,
			InstanceKey:    expanded.key,
			RepetitionData: expanded.repetitionData,
			Config:         n.Config,
			Runtime:        expanded.runtime,
		}
		g.Add(instance)
		refTargets := map[string]dag.Vertex{}
		locals := make([]*NodeStepLocal, 0, len(n.Config.Locals))
		actions := make([]*NodeStepAction, 0, len(n.Config.Actions))
		dataNodes := make([]*NodeStepData, 0, len(n.Config.DataSources))
		listNodes := make([]*NodeStepList, 0, len(n.Config.ListResources))
		for _, action := range n.Config.Actions {
			child := &NodeStepAction{Step: instance, Action: action}
			g.Add(child)
			g.Connect(dag.BasicEdge(instance, child))
			refTargets[terraformaddrs.Action{Type: action.Type, Name: action.Name}.String()] = child
			actions = append(actions, child)
		}
		for _, data := range n.Config.DataSources {
			child := &NodeStepData{Step: instance, Data: data, RefreshAtApply: isDynamicDataSource(n.Config, data)}
			g.Add(child)
			g.Connect(dag.BasicEdge(instance, child))
			refTargets[terraformaddrs.Resource{Mode: terraformaddrs.DataResourceMode, Type: data.Type, Name: data.Name}.String()] = child
			dataNodes = append(dataNodes, child)
		}
		for _, list := range n.Config.ListResources {
			child := &NodeStepList{Step: instance, List: list}
			g.Add(child)
			g.Connect(dag.BasicEdge(instance, child))
			refTargets[terraformaddrs.Resource{Mode: terraformaddrs.ListResourceMode, Type: list.Type, Name: list.Name}.String()] = child
			listNodes = append(listNodes, child)
		}
		for _, local := range n.Config.Locals {
			child := &NodeStepLocal{Step: instance, Local: local}
			g.Add(child)
			g.Connect(dag.BasicEdge(instance, child))
			refTargets[terraformaddrs.LocalValue{Name: local.Name}.String()] = child
			locals = append(locals, child)
		}
		executions := make([]*NodeStepExecution, 0, len(n.Config.Executions))
		for index, execution := range n.Config.Executions {
			child := &NodeStepExecution{Step: instance, Index: index, Execution: execution}
			g.Add(child)
			g.Connect(dag.BasicEdge(instance, child))
			executions = append(executions, child)
		}
		preconditions := make([]*NodeStepCondition, 0, len(n.Config.Preconditions))
		for _, condition := range n.Config.Preconditions {
			child := &NodeStepCondition{Step: instance, Condition: condition}
			g.Add(child)
			g.Connect(dag.BasicEdge(instance, child))
			preconditions = append(preconditions, child)
		}
		postconditions := make([]*NodeStepCondition, 0, len(n.Config.Postconditions))
		for _, condition := range n.Config.Postconditions {
			child := &NodeStepCondition{Step: instance, Condition: condition}
			g.Add(child)
			g.Connect(dag.BasicEdge(instance, child))
			postconditions = append(postconditions, child)
		}
		outputs := make([]*NodeStepOutput, 0, len(n.Config.Outputs))
		for _, output := range n.Config.Outputs {
			child := &NodeStepOutput{Step: instance, Output: output}
			g.Add(child)
			g.Connect(dag.BasicEdge(instance, child))
			refTargets[runbookaddrs.StepOutput{Step: runbookaddrs.StepInstance{StepName: n.StepName, InstanceKey: expanded.key}, OutputName: output.Name}.String()] = child
			outputs = append(outputs, child)
		}
		finalize := &NodeStepFinalize{Step: instance}
		g.Add(finalize)
		g.Connect(dag.BasicEdge(finalize, instance))
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
		for _, condition := range preconditions {
			connectStepReferences(&g, n.StepName, condition, referencesForStepCondition(condition.Condition), refTargets)
		}
		for _, condition := range postconditions {
			connectStepReferences(&g, n.StepName, condition, referencesForStepCondition(condition.Condition), refTargets)
			for _, execution := range executions {
				g.Connect(dag.BasicEdge(condition, execution))
			}
		}
		for _, output := range outputs {
			connectStepReferences(&g, n.StepName, output, referencesForStepOutput(output.Output), refTargets)
			// Outputs referencing dynamic data sources must wait for the
			// execution node that refreshes them via read_datasource.
			if outputReferencesDynamicData(output.Output, n.Config) {
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
	}
	if err := (&terraform.RootTransformer{}).Transform(&g); err != nil {
		return nil, tfdiags.Diagnostics{}.Append(err)
	}
	return &g, nil
}

func connectStepReferences(g *terraform.Graph, currentStep string, from dag.Vertex, refs []runbookaddrs.Referenceable, targets map[string]dag.Vertex) {
	for _, ref := range refs {
		if stepRef, ok := ref.(runbookaddrs.Step); ok {
			stepName := stepRef.Step.StepName
			if stepName == "" {
				stepName = currentStep
			}
			for key, dep := range targets {
				outputRef, diags := runbookaddrs.ParseRef(mustTraversalForRefKey(key))
				if diags.HasErrors() || outputRef == nil {
					continue
				}
				stepOutput, ok := outputRef.Subject.(runbookaddrs.StepOutput)
				if !ok || stepOutput.Step.StepName != stepName {
					continue
				}
				g.Connect(dag.BasicEdge(from, dep))
			}
			continue
		}
		key := ref.String()
		if stepOutput, ok := ref.(runbookaddrs.StepOutput); ok && stepOutput.Step.StepName == "" {
			key = runbookaddrs.StepOutput{Step: runbookaddrs.StepInstance{StepName: currentStep}, OutputName: stepOutput.OutputName}.String()
		}
		dep, ok := targets[key]
		if !ok {
			continue
		}
		g.Connect(dag.BasicEdge(from, dep))
	}
}

func mustTraversalForRefKey(key string) hcl.Traversal {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte(key), "", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return nil
	}
	return traversal
}

var (
	_ dag.Vertex                 = (*NodeExpandStep)(nil)
	_ GraphNodeDynamicExpandable = (*NodeExpandStep)(nil)
)
