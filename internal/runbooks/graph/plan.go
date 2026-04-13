package runbookgraph

import (
	"fmt"
	"sort"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/dag"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runtime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type PlanBuilder struct {
	Config       *runbookconfigs.RunbookConfig
	InputValues  terraform.InputValues
	StepsRuntime map[string]*runtime.Step
}

func NewPlan(config *runbookconfigs.RunbookConfig) (*terraform.Graph, tfdiags.Diagnostics) {
	return (&PlanBuilder{Config: config}).Build()
}

func (b *PlanBuilder) Build() (*terraform.Graph, tfdiags.Diagnostics) {
	return (&terraform.BasicGraphBuilder{
		Steps: b.Steps(),
		Name:  "RunbookPlanBuilder",
	}).Build(addrs.RootModuleInstance)
}

func (b *PlanBuilder) Steps() []terraform.GraphTransformer {
	config := rootRunbookConfig(b.Config)

	steps := []terraform.GraphTransformer{
		&terraform.RootVariableTransformer{
			Config:    config,
			RawValues: b.InputValues,
		},
		&PlanStepTransformer{Config: b.Config},
		&terraform.OutputTransformer{
			Config:   config,
			Planning: true,
		},
		&terraform.RootTransformer{},
	}

	if b.StepsRuntime != nil {
		steps = append(steps, &StepDetailsTransformer{
			Config: b.Config,
			Steps:  b.StepsRuntime,
		})
	}

	steps = append(steps, &terraform.TransitiveReductionTransformer{})

	return steps
}

func stepNodesByName(g *terraform.Graph) map[string]*NodeStep {
	ret := make(map[string]*NodeStep)
	for _, vertex := range g.Vertices() {
		node, ok := vertex.(*NodeStep)
		if !ok {
			continue
		}
		ret[node.StepName] = node
	}
	return ret
}

func rootRunbookConfig(config *runbookconfigs.RunbookConfig) *configs.Config {
	if config == nil {
		return nil
	}

	root := configs.NewEmptyConfig()
	root.Module.Variables = config.Variables
	root.Module.Outputs = config.Outputs
	return root
}

func sortVariables(in map[string]*configs.Variable) []*configs.Variable {
	names := sortNames(in)
	ret := make([]*configs.Variable, 0, len(names))
	for _, name := range names {
		ret = append(ret, in[name])
	}
	return ret
}

func sortSteps(in map[string]*runbookconfigs.Step) []*runbookconfigs.Step {
	names := sortNames(in)
	ret := make([]*runbookconfigs.Step, 0, len(names))
	for _, name := range names {
		ret = append(ret, in[name])
	}
	return ret
}

func sortOutputs(in map[string]*configs.Output) []*configs.Output {
	names := sortNames(in)
	ret := make([]*configs.Output, 0, len(names))
	for _, name := range names {
		ret = append(ret, in[name])
	}
	return ret
}

func sortNames[T any](in map[string]T) []string {
	names := make([]string, 0, len(in))
	for name := range in {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func replaceVertex(g *terraform.Graph, original, replacement dag.Vertex) error {
	if ok := g.Replace(original, replacement); !ok {
		return fmt.Errorf("failed to replace %s with %s", dag.VertexName(original), dag.VertexName(replacement))
	}
	return nil
}

type PlanStepTransformer struct {
	Config *runbookconfigs.RunbookConfig
}

func (t *PlanStepTransformer) Transform(g *terraform.Graph) error {
	if t == nil || t.Config == nil {
		return nil
	}

	for _, step := range sortSteps(t.Config.Steps) {
		g.Add(&NodeStep{StepName: step.Name})
	}

	return nil
}
