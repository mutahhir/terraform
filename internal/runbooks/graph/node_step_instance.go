package runbookgraph

import (
	"fmt"

	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runtime "github.com/hashicorp/terraform/internal/runbooks/runtime"
)

type NodeStepInstance struct {
	// NodeStepInstance represents a concrete runtime step instance created from
	// a declared step, even when there is only one instance.
	StepName string
	Config   *runbookconfigs.Step
	Runtime  *runtime.Step
}

func (n *NodeStepInstance) Hashcode() interface{} {
	return [2]string{"step_instance", n.StepName}
}

func (n *NodeStepInstance) Name() string {
	return fmt.Sprintf("step.%s", n.StepName)
}
