package runbookgraph

import (
	"github.com/hashicorp/hcl/v2"
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
			stepName := ""
			switch r := ref.(type) {
			case runbookaddrs.StepOutput:
				stepName = r.Step.StepName
			case runbookaddrs.Step:
				stepName = r.Step.StepName
			default:
				continue
			}
			// TODO: We need to catch this situation within a validation pass
			if stepName == "" {
				continue
			}
			dep, ok := stepNodes[stepName]
			if !ok {
				continue
			}
			g.Connect(dag.BasicEdge(expand, dep))
		}
	}
	return nil
}

// referencesForStep returns all the external references that this step's
// contents need — i.e., the other steps this step depends on. It crawls
// every expression within the step config (count, for_each, locals, actions,
// data sources, executions, conditions, outputs) and extracts traversals that
// resolve to other step addresses. These are used by
// StepOutputReferenceTransformer to wire dependency edges in the graph.
func referencesForStep(step *runbookconfigs.Step) []runbookaddrs.Referenceable {
	if step == nil {
		return nil
	}

	var refs []runbookaddrs.Referenceable
	refs = append(refs, referencesInExpr(step.Count)...)
	refs = append(refs, referencesInExpr(step.ForEach)...)
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
	for _, traversal := range step.DependsOn {
		// depends_on traversals are validated to be step.<name> at parse time
		if len(traversal) >= 2 {
			if nameAttr, ok := traversal[1].(hcl.TraverseAttr); ok {
				refs = append(refs, runbookaddrs.Step{
					Step: runbookaddrs.StepInstance{StepName: nameAttr.Name},
				})
			}
		}
	}
	return refs
}
