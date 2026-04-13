package runbookgraph

import (
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runtime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terraform"
)

type StepDetailsTransformer struct {
	Config *runbookconfigs.RunbookConfig
	Steps  map[string]*runtime.Step
}

func (t *StepDetailsTransformer) Transform(g *terraform.Graph) error {
	if t == nil || t.Config == nil {
		return nil
	}

	for _, vertex := range g.Vertices() {
		node, ok := vertex.(*NodeStep)
		if !ok {
			continue
		}

		replacement := &NodeStepDetails{
			StepName: node.StepName,
			Config:   t.Config.Steps[node.StepName],
		}
		if t != nil && t.Steps != nil {
			replacement.Step = t.Steps[node.StepName]
		}

		if err := replaceVertex(g, node, replacement); err != nil {
			return err
		}
	}

	return nil
}
