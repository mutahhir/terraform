package runbookgraph

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

func TestValidateReadDataSourceRefsMustReferenceDeclaredData(t *testing.T) {
	config := &runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"deploy": {
				Name: "deploy",
				DataSources: []*configs.Resource{
					{Type: "aws_lambda_function", Name: "existing", Mode: terraformaddrs.DataResourceMode},
				},
				Executions: []*runbookconfigs.Execution{
					{
						ReadDataSources: []hcl.Traversal{
							mustParseTraversal(t, `data.aws_lambda_function.existing`),
						},
					},
				},
			},
		},
	}

	diags := validateReadDataSourceRefs(config)
	if diags.HasErrors() {
		t.Fatalf("expected no errors for valid read ref, got: %s", diags.Err())
	}
}

func TestValidateReadDataSourceRefsRejectsUndeclaredData(t *testing.T) {
	config := &runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"deploy": {
				Name:        "deploy",
				DataSources: []*configs.Resource{},
				Executions: []*runbookconfigs.Execution{
					{
						ReadDataSources: []hcl.Traversal{
							mustParseTraversal(t, `data.aws_lambda_function.missing`),
						},
					},
				},
			},
		},
	}

	diags := validateReadDataSourceRefs(config)
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for undeclared data source, got none")
	}
}

func TestValidateReadDataSourceRefsRejectsNonDataReference(t *testing.T) {
	config := &runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"deploy": {
				Name: "deploy",
				Executions: []*runbookconfigs.Execution{
					{
						ReadDataSources: []hcl.Traversal{
							mustParseTraversal(t, `action.http.notify`),
						},
					},
				},
			},
		},
	}

	diags := validateReadDataSourceRefs(config)
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for non-data-source reference, got none")
	}
}

func TestValidateNoDynamicDataInCount(t *testing.T) {
	config := &runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"discover": {
				Name: "discover",
				DataSources: []*configs.Resource{
					{Type: "aws_lambda_function", Name: "targets", Mode: terraformaddrs.DataResourceMode},
				},
				Executions: []*runbookconfigs.Execution{
					{
						ReadDataSources: []hcl.Traversal{
							mustParseTraversal(t, `data.aws_lambda_function.targets`),
						},
					},
				},
				Count: mustParseExpression(t, `length(data.aws_lambda_function.targets.results)`),
			},
		},
	}

	diags := validateNoDynamicDataInExpansion(config)
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for dynamic data source in count, got none")
	}
}

func TestValidateNoDynamicDataInForEach(t *testing.T) {
	config := &runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"discover": {
				Name: "discover",
				DataSources: []*configs.Resource{
					{Type: "aws_lambda_function", Name: "targets", Mode: terraformaddrs.DataResourceMode},
				},
				Executions: []*runbookconfigs.Execution{
					{
						ReadDataSources: []hcl.Traversal{
							mustParseTraversal(t, `data.aws_lambda_function.targets`),
						},
					},
				},
				ForEach: mustParseExpression(t, `data.aws_lambda_function.targets.results`),
			},
		},
	}

	diags := validateNoDynamicDataInExpansion(config)
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for dynamic data source in for_each, got none")
	}
}

func TestValidateNoDynamicDataAllowsNonDynamicInCount(t *testing.T) {
	config := &runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"deploy": {
				Name: "deploy",
				DataSources: []*configs.Resource{
					{Type: "aws_lambda_function", Name: "targets", Mode: terraformaddrs.DataResourceMode},
				},
				// No read — this data source is NOT dynamic
				Executions: []*runbookconfigs.Execution{},
				Count:      mustParseExpression(t, `length(data.aws_lambda_function.targets.results)`),
			},
		},
	}

	diags := validateNoDynamicDataInExpansion(config)
	if diags.HasErrors() {
		t.Fatalf("expected no errors for non-dynamic data source in count, got: %s", diags.Err())
	}
}

func TestValidateNoDynamicDataCrossStepTaint(t *testing.T) {
	config := &runbookconfigs.RunbookConfig{
		Steps: map[string]*runbookconfigs.Step{
			"discover": {
				Name: "discover",
				DataSources: []*configs.Resource{
					{Type: "aws_lambda_function", Name: "created", Mode: terraformaddrs.DataResourceMode},
				},
				Executions: []*runbookconfigs.Execution{
					{
						ReadDataSources: []hcl.Traversal{
							mustParseTraversal(t, `data.aws_lambda_function.created`),
						},
					},
				},
				Outputs: []*configs.Output{
					{Name: "arn", Expr: mustParseExpression(t, `data.aws_lambda_function.created.arn`)},
				},
			},
			"deploy": {
				Name:    "deploy",
				ForEach: mustParseExpression(t, `step.discover.arn`),
			},
		},
	}

	diags := validateNoDynamicDataInExpansion(config)
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for cross-step tainted output in for_each, got none")
	}
}

func TestConfigParseReadDataSourceBlock(t *testing.T) {
	src := `
execute {
  invoke_action {
    action = action.lambda.create
  }
  read {
    datasource = data.aws_lambda_function.created
  }
}
`
	file, diags := hclsyntax.ParseConfig([]byte(src), "test.hcl", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		t.Fatalf("parse error: %s", diags.Error())
	}

	content, _ := file.Body.Content(&hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{{Type: "execute"}},
	})
	if len(content.Blocks) == 0 {
		t.Fatal("expected execute block")
	}

	exec, execDiags := runbookconfigs.DecodeExecutionBlockForTest(content.Blocks[0])
	if hclDiagsToTfDiags(execDiags).HasErrors() {
		t.Fatalf("decode error: %s", execDiags.Error())
	}

	if len(exec.InvokeAction) != 1 {
		t.Fatalf("expected 1 invoke_action, got %d", len(exec.InvokeAction))
	}
	if len(exec.ReadDataSources) != 1 {
		t.Fatalf("expected 1 read, got %d", len(exec.ReadDataSources))
	}
	if len(exec.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(exec.Operations))
	}
	if exec.Operations[0].Type != runbookconfigs.ExecuteOpInvokeAction {
		t.Fatalf("expected first op to be invoke_action, got %s", exec.Operations[0].Type)
	}
	if exec.Operations[1].Type != runbookconfigs.ExecuteOpReadDataSource {
		t.Fatalf("expected second op to be read, got %s", exec.Operations[1].Type)
	}
}

func hclDiagsToTfDiags(diags hcl.Diagnostics) tfdiags.Diagnostics {
	var ret tfdiags.Diagnostics
	for _, d := range diags {
		ret = ret.Append(d)
	}
	return ret
}
