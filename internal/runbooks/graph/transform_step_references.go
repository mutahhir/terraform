package runbookgraph

import (
	"github.com/hashicorp/terraform/internal/dag"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
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

		for _, ref := range referencesForStep(expand.Config) {
			stepOutput, ok := ref.(runbookaddrs.StepOutput)
			if !ok || stepOutput.Step.StepName == "" {
				continue
			}
			dep, ok := stepNodes[stepOutput.Step.StepName]
			if !ok {
				continue
			}
			g.Connect(dag.BasicEdge(expand, dep))
		}
	}
	return nil
}

func referencesForStep(step *runbookconfigs.Step) []runbookaddrs.Referenceable {
	if step == nil {
		return nil
	}

	var refs []runbookaddrs.Referenceable
	for _, local := range step.Locals {
		refs = append(refs, referencesForStepLocal(local)...)
	}
	for _, action := range step.Actions {
		refs = append(refs, referencesForStepAction(action)...)
	}
	for _, data := range step.DataSources {
		refs = append(refs, referencesForStepResource(data)...)
	}
	for _, list := range step.ListResources {
		refs = append(refs, referencesForStepResource(list)...)
	}
	for _, execution := range step.Executions {
		refs = append(refs, referencesForStepExecution(execution)...)
	}
	for _, condition := range step.Preconditions {
		refs = append(refs, referencesForStepCondition(condition)...)
	}
	for _, condition := range step.Postconditions {
		refs = append(refs, referencesForStepCondition(condition)...)
	}
	for _, output := range step.Outputs {
		refs = append(refs, referencesForStepOutput(output)...)
	}
	return refs
}
