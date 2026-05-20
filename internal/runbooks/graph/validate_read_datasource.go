package runbookgraph

import (
	"github.com/hashicorp/hcl/v2"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	runbookaddrs "github.com/hashicorp/terraform/internal/runbooks/addrs"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// outputReferencesDynamicData returns true if the output expression references
// a data source that has a read_datasource directive in the step.
func outputReferencesDynamicData(output *configs.Output, step *runbookconfigs.Step) bool {
	if output == nil || output.Expr == nil || step == nil {
		return false
	}
	for _, traversal := range output.Expr.Variables() {
		ref, diags := runbookaddrs.ParseRef(traversal)
		if diags.HasErrors() || ref == nil {
			continue
		}
		if resource, ok := ref.Subject.(terraformaddrs.Resource); ok && resource.Mode == terraformaddrs.DataResourceMode {
			if isDynamicDataSource(step, findDataSourceByAddr(step, resource)) {
				return true
			}
		}
	}
	return false
}

func findDataSourceByAddr(step *runbookconfigs.Step, addr terraformaddrs.Resource) *configs.Resource {
	if step == nil {
		return nil
	}
	for _, ds := range step.DataSources {
		if ds != nil && ds.Type == addr.Type && ds.Name == addr.Name {
			return ds
		}
	}
	return nil
}

// isDynamicDataSource returns true if any execute block in the step has a
// read_datasource directive referencing this data source.
func isDynamicDataSource(step *runbookconfigs.Step, data *configs.Resource) bool {
	if step == nil || data == nil {
		return false
	}
	addr := data.Addr().String()
	for _, exec := range step.Executions {
		for _, traversal := range exec.ReadDataSources {
			ref, diags := runbookaddrs.ParseRef(traversal)
			if diags.HasErrors() || ref == nil {
				continue
			}
			if resource, ok := ref.Subject.(terraformaddrs.Resource); ok && resource.String() == addr {
				return true
			}
		}
	}
	return false
}

// validateReadDataSourceRefs checks that each read_datasource traversal in
// execute blocks references a declared step-level data source.
func validateReadDataSourceRefs(config *runbookconfigs.RunbookConfig) tfdiags.Diagnostics {
	if config == nil {
		return nil
	}
	var diags tfdiags.Diagnostics
	for _, step := range config.Steps {
		if step == nil {
			continue
		}
		declared := map[string]bool{}
		for _, ds := range step.DataSources {
			declared[ds.Addr().String()] = true
		}
		for _, exec := range step.Executions {
			for _, traversal := range exec.ReadDataSources {
				ref, refDiags := runbookaddrs.ParseRef(traversal)
				if refDiags.HasErrors() {
					diags = diags.Append(refDiags)
					continue
				}
				if ref == nil {
					continue
				}
				resource, ok := ref.Subject.(terraformaddrs.Resource)
				if !ok || resource.Mode != terraformaddrs.DataResourceMode {
					diags = diags.Append(&hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Invalid read_datasource reference",
						Detail:   "The datasource attribute must reference a data source (e.g., data.aws_lambda_function.created).",
						Subject:  traversal.SourceRange().Ptr(),
					})
					continue
				}
				if !declared[resource.String()] {
					diags = diags.Append(&hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Undeclared data source in read_datasource",
						Detail:   "The data source " + resource.String() + " is not declared in this step. read_datasource can only reference data sources declared at the step level.",
						Subject:  traversal.SourceRange().Ptr(),
					})
				}
			}
		}
	}
	return diags
}

// collectDynamicDataAddrs returns the set of data source address strings that
// are referenced by any read_datasource directive across all steps.
func collectDynamicDataAddrs(config *runbookconfigs.RunbookConfig) map[string]bool {
	addrs := map[string]bool{}
	if config == nil {
		return addrs
	}
	for _, step := range config.Steps {
		if step == nil {
			continue
		}
		for _, exec := range step.Executions {
			for _, traversal := range exec.ReadDataSources {
				ref, diags := runbookaddrs.ParseRef(traversal)
				if diags.HasErrors() || ref == nil {
					continue
				}
				if resource, ok := ref.Subject.(terraformaddrs.Resource); ok && resource.Mode == terraformaddrs.DataResourceMode {
					addrs[resource.String()] = true
				}
			}
		}
	}
	return addrs
}

// validateNoDynamicDataInExpansion checks that no count, for_each, or
// precondition expression references a dynamic data source (one that has a
// read_datasource directive) either directly or transitively through step
// outputs.
func validateNoDynamicDataInExpansion(config *runbookconfigs.RunbookConfig) tfdiags.Diagnostics {
	if config == nil {
		return nil
	}
	dynamicAddrs := collectDynamicDataAddrs(config)
	if len(dynamicAddrs) == 0 {
		return nil
	}

	// Build a set of tainted outputs per step: outputs that transitively depend
	// on a dynamic data source (through locals or direct reference).
	taintedOutputs := buildTaintedOutputs(config, dynamicAddrs)

	var diags tfdiags.Diagnostics
	for _, step := range config.Steps {
		if step == nil {
			continue
		}
		if step.Count != nil {
			diags = diags.Append(checkExprNotTaintedByDynamic(step.Count, step, config, dynamicAddrs, taintedOutputs, "count"))
		}
		if step.ForEach != nil {
			diags = diags.Append(checkExprNotTaintedByDynamic(step.ForEach, step, config, dynamicAddrs, taintedOutputs, "for_each"))
		}
		for _, cond := range step.Preconditions {
			if cond != nil && cond.Condition != nil {
				diags = diags.Append(checkExprNotTaintedByDynamic(cond.Condition, step, config, dynamicAddrs, taintedOutputs, "precondition"))
			}
		}
	}
	return diags
}

// buildTaintedOutputs identifies step outputs that transitively depend on
// dynamic data sources. Returns a map of "stepName.outputName" → true.
func buildTaintedOutputs(config *runbookconfigs.RunbookConfig, dynamicAddrs map[string]bool) map[string]bool {
	tainted := map[string]bool{}
	for stepName, step := range config.Steps {
		if step == nil {
			continue
		}
		// Find locals that reference dynamic data sources
		taintedLocals := map[string]bool{}
		for _, local := range step.Locals {
			if local == nil || local.Expr == nil {
				continue
			}
			if exprReferencesDynamicData(local.Expr, dynamicAddrs) {
				taintedLocals[local.Name] = true
			}
		}
		// Find outputs that reference dynamic data or tainted locals
		for _, output := range step.Outputs {
			if output == nil || output.Expr == nil {
				continue
			}
			if exprReferencesDynamicData(output.Expr, dynamicAddrs) || exprReferencesTaintedLocal(output.Expr, taintedLocals) {
				tainted[stepName+"."+output.Name] = true
			}
		}
	}
	return tainted
}

// checkExprNotTaintedByDynamic verifies an expression does not reference dynamic
// data sources (directly or through cross-step tainted outputs).
func checkExprNotTaintedByDynamic(
	expr hcl.Expression,
	step *runbookconfigs.Step,
	config *runbookconfigs.RunbookConfig,
	dynamicAddrs map[string]bool,
	taintedOutputs map[string]bool,
	context string,
) tfdiags.Diagnostics {
	if expr == nil {
		return nil
	}
	var diags tfdiags.Diagnostics
	for _, traversal := range expr.Variables() {
		ref, refDiags := runbookaddrs.ParseRef(traversal)
		if refDiags.HasErrors() || ref == nil {
			continue
		}
		// Direct reference to a dynamic data source within the same step
		if resource, ok := ref.Subject.(terraformaddrs.Resource); ok && resource.Mode == terraformaddrs.DataResourceMode {
			if dynamicAddrs[resource.String()] {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Dynamic data source in " + context,
					Detail:   "The " + context + " expression references " + resource.String() + " which has a read_datasource directive. Dynamic data sources cannot be used in expressions that determine graph shape.",
					Subject:  traversal.SourceRange().Ptr(),
				})
			}
		}
		// Cross-step reference to a tainted output
		if stepOutput, ok := ref.Subject.(runbookaddrs.StepOutput); ok {
			key := stepOutput.Step.StepName + "." + stepOutput.OutputName
			if taintedOutputs[key] {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Dynamic data dependency in " + context,
					Detail:   "The " + context + " expression references step." + key + " which depends on a dynamic data source (one with a read_datasource directive). Dynamic data cannot influence graph shape.",
					Subject:  traversal.SourceRange().Ptr(),
				})
			}
		}
	}
	return diags
}

// exprReferencesDynamicData checks if an expression directly references any
// data source in the dynamicAddrs set.
func exprReferencesDynamicData(expr hcl.Expression, dynamicAddrs map[string]bool) bool {
	if expr == nil {
		return false
	}
	for _, traversal := range expr.Variables() {
		ref, diags := runbookaddrs.ParseRef(traversal)
		if diags.HasErrors() || ref == nil {
			continue
		}
		if resource, ok := ref.Subject.(terraformaddrs.Resource); ok && resource.Mode == terraformaddrs.DataResourceMode {
			if dynamicAddrs[resource.String()] {
				return true
			}
		}
	}
	return false
}

// exprReferencesTaintedLocal checks if an expression references any local value
// in the taintedLocals set.
func exprReferencesTaintedLocal(expr hcl.Expression, taintedLocals map[string]bool) bool {
	if expr == nil || len(taintedLocals) == 0 {
		return false
	}
	for _, traversal := range expr.Variables() {
		ref, diags := runbookaddrs.ParseRef(traversal)
		if diags.HasErrors() || ref == nil {
			continue
		}
		if local, ok := ref.Subject.(terraformaddrs.LocalValue); ok {
			if taintedLocals[local.Name] {
				return true
			}
		}
	}
	return false
}
