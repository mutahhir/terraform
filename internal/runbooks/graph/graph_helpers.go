package runbookgraph

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/dag"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	"github.com/hashicorp/terraform/internal/terraform"
)

// subsumeExpandedGraph merges all vertices and edges from expanded into parent.
// Used by the saved plan graph builder to construct a pre-expanded flat graph
// at build time.
func subsumeExpandedGraph(parent, expanded *terraform.Graph) {
	if parent == nil || expanded == nil {
		return
	}
	parent.Subsume(&expanded.AcyclicGraph.Graph)
}

// rewireExactStepOutputReferences replaces coarse expand-node edges with precise
// output-node edges. Used by saved plan graph construction where the graph is
// pre-expanded and inner nodes from different steps coexist in the same graph.
func rewireExactStepOutputReferences(graph *terraform.Graph, vertex dag.Vertex) {
	if graph == nil || vertex == nil {
		return
	}
	refs := referencesForVertex(vertex)
	if len(refs) == 0 {
		return
	}
	currentStep, _ := stepNameForVertex(vertex)
	for _, ref := range refs {
		stepName := ""
		var targets []*NodeStepOutput
		switch r := ref.(type) {
		case runbookaddrs.StepOutput:
			stepName = r.Step.StepName
			if stepName == "" || stepName == currentStep {
				continue
			}
			targets = matchingStepOutputVertices(graph, r)
		case runbookaddrs.Step:
			stepName = r.Step.StepName
			if stepName == "" || stepName == currentStep {
				continue
			}
			targets = allStepOutputVertices(graph, r.Step)
		default:
			continue
		}
		if len(targets) == 0 {
			continue
		}
		graph.RemoveEdge(dag.BasicEdge(vertex, &NodeExpandStep{StepName: stepName}))
		for _, target := range targets {
			graph.Connect(dag.BasicEdge(vertex, target))
		}
	}
}

func referencesForVertex(vertex dag.Vertex) []runbookaddrs.Referenceable {
	switch node := vertex.(type) {
	case *NodeExpandStep:
		if node == nil || node.Config == nil {
			return nil
		}
		return referencesForStep(node.Config)
	case *NodeStepAction:
		return referencesForStepAction(node.Action)
	case *NodeStepData:
		return referencesForStepResource(node.Data)
	case *NodeStepList:
		return referencesForStepResource(node.List)
	case *NodeStepLocal:
		return referencesForStepLocal(node.Local)
	case *NodeStepExecution:
		return referencesForStepExecution(node.Execution)
	case *NodeStepCondition:
		return referencesForStepCondition(node.Condition)
	case *NodeStepOutput:
		return referencesForStepOutput(node.Output)
	default:
		return nil
	}
}

func matchingStepOutputVertices(graph *terraform.Graph, ref runbookaddrs.StepOutput) []*NodeStepOutput {
	if graph == nil || ref.Step.StepName == "" {
		return nil
	}
	var ret []*NodeStepOutput
	for _, vertex := range graph.Vertices() {
		node, ok := vertex.(*NodeStepOutput)
		if !ok || node.Step == nil || node.Output == nil {
			continue
		}
		if node.Step.StepName != ref.Step.StepName || node.Output.Name != ref.OutputName {
			continue
		}
		if ref.Step.InstanceKey != nil && ref.Step.InstanceKey != terraformaddrs.NoKey {
			if node.Step.InstanceKey != ref.Step.InstanceKey {
				continue
			}
		}
		ret = append(ret, node)
	}
	return ret
}

func allStepOutputVertices(graph *terraform.Graph, step runbookaddrs.StepInstance) []*NodeStepOutput {
	if graph == nil || step.StepName == "" {
		return nil
	}
	var ret []*NodeStepOutput
	for _, vertex := range graph.Vertices() {
		node, ok := vertex.(*NodeStepOutput)
		if !ok || node.Step == nil || node.Output == nil {
			continue
		}
		if node.Step.StepName != step.StepName {
			continue
		}
		if step.InstanceKey != nil && step.InstanceKey != terraformaddrs.NoKey && node.Step.InstanceKey != step.InstanceKey {
			continue
		}
		ret = append(ret, node)
	}
	return ret
}

func mustTraversalForRefKey(key string) hcl.Traversal {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte(key), "", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return nil
	}
	return traversal
}
