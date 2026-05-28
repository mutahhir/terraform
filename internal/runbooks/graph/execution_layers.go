package runbookgraph

import (
	"github.com/hashicorp/terraform/internal/dag"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/terraform"
)

// ExecutionLayer represents a group of steps that execute at the same point
// in time. If the layer contains more than one step, they run in parallel.
type ExecutionLayer struct {
	Steps []ExecutionLayerStep
}

// IsParallel returns true if this layer contains multiple steps that will
// execute concurrently.
func (l ExecutionLayer) IsParallel() bool {
	return len(l.Steps) > 1
}

// ExecutionLayerStep represents a single step within an execution layer.
type ExecutionLayerStep struct {
	Name        string
	InstanceKey string
	Skipped     bool
	SkipReason  string
}

// ComputeExecutionLayers groups step vertices from the plan graph by their
// topological depth. Steps at the same depth have no inter-dependencies and
// will execute concurrently via the DAG walker.
//
// The algorithm: BFS from roots (no incoming edges from other steps), assigning
// each step depth = max(depths of step-dependencies) + 1. Group by depth.
func ComputeExecutionLayers(graph *terraform.Graph) []ExecutionLayer {
	if graph == nil {
		return nil
	}

	// Collect only the step-expand nodes (top-level steps in the graph).
	// These are the nodes that represent user-declared steps.
	stepNodes := make(map[dag.Vertex]*NodeExpandStep)
	for _, v := range graph.Vertices() {
		if node, ok := v.(*NodeExpandStep); ok {
			stepNodes[v] = node
		}
	}

	if len(stepNodes) == 0 {
		return nil
	}

	// Compute depth for each step node based on inter-step dependencies.
	// A step's depth = max(depths of step-dependencies) + 1.
	// Steps with no step-dependencies have depth 0.
	depths := make(map[dag.Vertex]int)
	var computeDepth func(v dag.Vertex) int
	computeDepth = func(v dag.Vertex) int {
		if d, ok := depths[v]; ok {
			return d
		}
		maxDep := -1
		for depVertex := range graph.DownEdges(v).List() {
			// Only consider edges to other step nodes
			if _, isStep := stepNodes[depVertex.(dag.Vertex)]; isStep {
				d := computeDepth(depVertex.(dag.Vertex))
				if d > maxDep {
					maxDep = d
				}
			}
		}
		depth := maxDep + 1
		depths[v] = depth
		return depth
	}

	for v := range stepNodes {
		computeDepth(v)
	}

	// Group by depth
	maxDepth := 0
	for _, d := range depths {
		if d > maxDepth {
			maxDepth = d
		}
	}

	layers := make([]ExecutionLayer, maxDepth+1)
	for v, node := range stepNodes {
		d := depths[v]
		layers[d].Steps = append(layers[d].Steps, ExecutionLayerStep{
			Name: node.StepName,
		})
	}

	// Sort steps within each layer for deterministic output
	for i := range layers {
		sortLayerSteps(layers[i].Steps)
	}

	return layers
}

func sortLayerSteps(steps []ExecutionLayerStep) {
	for i := 1; i < len(steps); i++ {
		for j := i; j > 0 && steps[j].Name < steps[j-1].Name; j-- {
			steps[j], steps[j-1] = steps[j-1], steps[j]
		}
	}
}

// annotateLayersWithStatus updates execution layer steps with skip status
// from the evaluated plan steps.
func annotateLayersWithStatus(plan *Plan) {
	if plan == nil || len(plan.ExecutionLayers) == 0 {
		return
	}
	stepsByName := make(map[string]*runbookruntime.Step, len(plan.Steps))
	for _, step := range plan.Steps {
		if step != nil {
			stepsByName[step.Name] = step
		}
	}
	for i := range plan.ExecutionLayers {
		for j := range plan.ExecutionLayers[i].Steps {
			ls := &plan.ExecutionLayers[i].Steps[j]
			if step, ok := stepsByName[ls.Name]; ok {
				if step.Status == runbookruntime.StepStatusSkipped {
					ls.Skipped = true
					ls.SkipReason = step.SkipReason
				}
				if step.InstanceKey != nil {
					ls.InstanceKey = step.InstanceKey.String()
				}
			}
		}
	}
}
