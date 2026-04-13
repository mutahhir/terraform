// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"testing"

	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/spf13/afero"
)

func TestPlanBuilderBuildIntegrationEmptyStepVariableAndOutput(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
variable "name" {
  type = string
}

output "summary" {
  value = var.name
}

step "deploy" {}
`)

	parser := runbookconfigs.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook", "/workspace")
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Error())
	}
	if config == nil {
		t.Fatal("expected config but got nil")
	}

	graph, buildDiags := (&PlanBuilder{Config: config}).Build()
	if buildDiags.HasErrors() {
		t.Fatalf("unexpected build diagnostics: %s", buildDiags.Err())
	}

	inputNode := rootVariableNode(graph, "name")
	stepNode := &NodeStep{StepName: "deploy"}
	outputNode := outputNode(graph, "summary")

	if inputNode == nil {
		t.Fatal("expected terraform root input variable node")
	}
	if !graph.HasVertex(stepNode) {
		t.Fatal("expected empty step node")
	}
	if outputNode == nil {
		t.Fatal("expected terraform output node")
	}
	if !hasVertexNamed(graph, "root") {
		t.Fatal("expected root node")
	}

	deps := graph.DownEdges(vertexNamed(graph, "root"))
	if !deps.Include(inputNode) {
		t.Fatal("expected root to connect to input variable")
	}
	if !deps.Include(stepNode) {
		t.Fatal("expected root to connect to empty step")
	}
	if !deps.Include(outputNode) {
		t.Fatal("expected root to connect to output")
	}
	if graph.DownEdges(outputNode).Len() != 0 {
		t.Fatal("expected output to have no dependencies when none are declared")
	}
	if len(config.Steps["deploy"].Actions) != 0 {
		t.Fatal("expected empty step to have no actions")
	}
	if len(config.Steps["deploy"].Executions) != 0 {
		t.Fatal("expected empty step to have no executions")
	}
	if len(config.Steps["deploy"].Outputs) != 0 {
		t.Fatal("expected empty step to have no outputs")
	}
}

func writeIntegrationTestFile(t *testing.T, fs afero.Fs, path, src string) {
	t.Helper()
	if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %s", path, err)
	}
}
