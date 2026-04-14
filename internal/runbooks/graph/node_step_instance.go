package runbookgraph

import (
	"fmt"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runtime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type NodeStepInstance struct {
	// NodeStepInstance represents a concrete runtime step instance created from
	// a declared step, even when there is only one instance.
	StepName       string
	InstanceKey    terraformaddrs.InstanceKey
	RepetitionData *terraform.InstanceKeyEvalData
	Config         *runbookconfigs.Step
	Runtime        *runtime.Step
}

func (n *NodeStepInstance) Hashcode() interface{} {
	key := ""
	if n.InstanceKey != nil {
		key = n.InstanceKey.String()
	}
	return [3]string{"step_instance", n.StepName, key}
}

func (n *NodeStepInstance) Name() string {
	if n.InstanceKey == nil {
		return fmt.Sprintf("step.%s", n.StepName)
	}
	return fmt.Sprintf("step.%s%s", n.StepName, n.InstanceKey.String())
}

func (n *NodeStepInstance) Execute(ctx *EvalContext, op walkOperation) tfdiags.Diagnostics {
	var step *runtime.Step
	switch op {
	case walkOperationPlan:
		step = ctx.ensurePlannedStepWithKey(n.StepName, n.InstanceKey, n.Config, n.Runtime)
		ctx.EmitPlannedStep(step)
	case walkOperationExecute:
		step = ctx.ensureRunningStepWithKey(n.StepName, n.InstanceKey, n.Config, n.Runtime)
	default:
		step = ctx.ensureStepWithKey(n.StepName, n.InstanceKey, n.Config, n.Runtime)
	}
	return nil
}
