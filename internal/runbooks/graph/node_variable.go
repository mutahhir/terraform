package runbookgraph

import (
	"fmt"

	"github.com/hashicorp/terraform/internal/configs"
)

type NodeInputVariable struct {
	Variable *configs.Variable
}

func (n *NodeInputVariable) Hashcode() interface{} {
	return [2]string{"input_variable", n.Variable.Name}
}

func (n *NodeInputVariable) Name() string {
	return fmt.Sprintf("var.%s", n.Variable.Name)
}

type NodeOutputVariable struct {
	Output *configs.Output
}

func (n *NodeOutputVariable) Hashcode() interface{} {
	return [2]string{"output_variable", n.Output.Name}
}

func (n *NodeOutputVariable) Name() string {
	return fmt.Sprintf("output.%s", n.Output.Name)
}
