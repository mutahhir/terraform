package runbookgraph

import (
	"github.com/hashicorp/terraform/internal/dag"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	"github.com/hashicorp/terraform/internal/terraform"
)

type StepOutputReferenceTransformer struct{}

func (t *StepOutputReferenceTransformer) Transform(g *terraform.Graph) error {
	stepNodes := stepNodesByName(g)
	for _, vertex := range g.Vertices() {
		expand, ok := vertex.(*NodeExpandStep)
		if !ok || expand.Config == nil {
			continue
		}

		for _, output := range expand.Config.Outputs {
			for _, ref := range referencesForStepOutput(output) {
				stepOutput, ok := ref.(runbookaddrs.StepOutput)
				if !ok || stepOutput.StepName == "" {
					continue
				}
				dep, ok := stepNodesByName(g)[stepOutput.StepName]
				if !ok {
					continue
				}
				g.Connect(dag.BasicEdge(expand, dep))
			}
		}
	}
	_ = stepNodes
	return nil
}
