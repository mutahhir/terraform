package runbookgraph

import (
	"fmt"

	"github.com/hashicorp/terraform/internal/configs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
)

type NodeStepAction struct {
	StepName string
	Action   *configs.Action
}

func (n *NodeStepAction) Hashcode() interface{} {
	return [4]string{"step_action", n.StepName, n.Action.Type, n.Action.Name}
}

func (n *NodeStepAction) Name() string {
	return fmt.Sprintf("step.%s.action.%s.%s", n.StepName, n.Action.Type, n.Action.Name)
}

type NodeStepData struct {
	StepName string
	Data     *configs.Resource
}

func (n *NodeStepData) Hashcode() interface{} {
	return [4]string{"step_data", n.StepName, n.Data.Type, n.Data.Name}
}

func (n *NodeStepData) Name() string {
	return fmt.Sprintf("step.%s.data.%s.%s", n.StepName, n.Data.Type, n.Data.Name)
}

type NodeStepList struct {
	StepName string
	List     *configs.Resource
}

func (n *NodeStepList) Hashcode() interface{} {
	return [4]string{"step_list", n.StepName, n.List.Type, n.List.Name}
}

func (n *NodeStepList) Name() string {
	return fmt.Sprintf("step.%s.list.%s.%s", n.StepName, n.List.Type, n.List.Name)
}

type NodeStepLocal struct {
	StepName string
	Local    *configs.Local
}

func (n *NodeStepLocal) Hashcode() interface{} {
	return [3]string{"step_local", n.StepName, n.Local.Name}
}

func (n *NodeStepLocal) Name() string {
	return fmt.Sprintf("step.%s.local.%s", n.StepName, n.Local.Name)
}

type NodeStepExecution struct {
	StepName  string
	Index     int
	Execution *runbookconfigs.Execution
}

func (n *NodeStepExecution) Hashcode() interface{} {
	return [3]interface{}{"step_execution", n.StepName, n.Index}
}

func (n *NodeStepExecution) Name() string {
	return fmt.Sprintf("step.%s.execute.%d", n.StepName, n.Index)
}

type NodeStepCondition struct {
	StepName  string
	Condition *runbookconfigs.Condition
}

func (n *NodeStepCondition) Hashcode() interface{} {
	return [3]interface{}{"step_condition", n.StepName, n.Condition.DeclRange.String()}
}

func (n *NodeStepCondition) Name() string {
	return fmt.Sprintf("step.%s.%s.%s", n.StepName, n.Condition.Kind, n.Condition.DeclRange.String())
}

type NodeStepOutput struct {
	StepName string
	Output   *configs.Output
}

func (n *NodeStepOutput) Hashcode() interface{} {
	return [3]string{"step_output", n.StepName, n.Output.Name}
}

func (n *NodeStepOutput) Name() string {
	return fmt.Sprintf("step.%s.%s", n.StepName, n.Output.Name)
}
