package runbookgraph

import (
	"fmt"

	"github.com/hashicorp/terraform/internal/dag"
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

func (n *NodeExpandStep) DynamicExpand(ctx terraform.EvalContext) (*terraform.Graph, tfdiags.Diagnostics) {
	var g terraform.Graph
	g.Add(&NodeStepInstance{
		StepName: n.StepName,
		Config:   n.Config,
		Runtime:  n.Runtime,
	})
	if err := (&terraform.RootTransformer{}).Transform(&g); err != nil {
		return nil, tfdiags.Diagnostics{}.Append(err)
	}
	return &g, nil
}

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

var (
	_ dag.Vertex                           = (*NodeExpandStep)(nil)
	_ terraform.GraphNodeDynamicExpandable = (*NodeExpandStep)(nil)
)
