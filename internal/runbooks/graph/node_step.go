package runbookgraph

import (
	"fmt"

	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runtime "github.com/hashicorp/terraform/internal/runbooks/runtime"
)

type NodeStep struct {
	StepName string
}

func (n *NodeStep) Hashcode() interface{} {
	return [2]string{"step", n.StepName}
}

func (n *NodeStep) Name() string {
	return fmt.Sprintf("step.%s", n.StepName)
}

type NodeStepDetails struct {
	StepName string
	Config   *runbookconfigs.Step
	Step     *runtime.Step
}

func (n *NodeStepDetails) Hashcode() interface{} {
	return [2]string{"step_details", n.StepName}
}

func (n *NodeStepDetails) Name() string {
	return fmt.Sprintf("step.%s", n.StepName)
}
