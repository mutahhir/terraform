// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"fmt"
	"maps"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookaddrs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
)

type RunbookContextOpts struct {
	Config *runbookconfig.RunbookConfig
}

type RunbookContext struct {
	config                   *runbookconfig.RunbookConfig
	variablesByName          map[string]*configs.Variable
	outputsByName            map[string]*configs.Output
	stepsByName              map[string]*runbookconfig.Step
	stepLocalsByStep         map[string]map[string]*configs.Local
	stepActionsByStep        map[string]map[string]*configs.Action
	stepDataSourcesByStep    map[string]map[string]*configs.Resource
	stepListsByStep          map[string]map[string]*configs.Resource
	stepOutputsByStep        map[string]map[string]*configs.Output
	usedWorkspaceActions     []runbookaddrs.ExecutableAction
	usedWorkspaceOutputNames []runbookaddrs.WorkspaceOutputValue
}

func NewContext(opts *RunbookContextOpts) (*RunbookContext, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	if opts == nil || opts.Config == nil {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid runbook context",
			Detail:   "A runbook runtime context requires a non-nil runbook configuration.",
		})
		return nil, diags
	}

	ctx := &RunbookContext{
		config:                   opts.Config,
		variablesByName:          make(map[string]*configs.Variable, len(opts.Config.Variables)),
		outputsByName:            make(map[string]*configs.Output, len(opts.Config.Outputs)),
		stepsByName:              make(map[string]*runbookconfig.Step, len(opts.Config.Steps)),
		stepLocalsByStep:         make(map[string]map[string]*configs.Local, len(opts.Config.Steps)),
		stepActionsByStep:        make(map[string]map[string]*configs.Action, len(opts.Config.Steps)),
		stepDataSourcesByStep:    make(map[string]map[string]*configs.Resource, len(opts.Config.Steps)),
		stepListsByStep:          make(map[string]map[string]*configs.Resource, len(opts.Config.Steps)),
		stepOutputsByStep:        make(map[string]map[string]*configs.Output, len(opts.Config.Steps)),
		usedWorkspaceActions:     make([]runbookaddrs.ExecutableAction, 0),
		usedWorkspaceOutputNames: make([]runbookaddrs.WorkspaceOutputValue, 0),
	}

	maps.Copy(ctx.variablesByName, opts.Config.Variables)
	maps.Copy(ctx.outputsByName, opts.Config.Outputs)

	for name, step := range opts.Config.Steps {
		ctx.stepsByName[name] = step

		locals := make(map[string]*configs.Local, len(step.Locals))
		for _, local := range step.Locals {
			locals[local.Name] = local
		}
		ctx.stepLocalsByStep[name] = locals

		actions := make(map[string]*configs.Action, len(step.Actions))
		for _, action := range step.Actions {
			actions[action.Addr().String()] = action
		}
		ctx.stepActionsByStep[name] = actions

		dataSources := make(map[string]*configs.Resource, len(step.DataSources))
		for _, dataSource := range step.DataSources {
			dataSources[dataSource.Addr().String()] = dataSource
		}
		ctx.stepDataSourcesByStep[name] = dataSources

		lists := make(map[string]*configs.Resource, len(step.ListResources))
		for _, list := range step.ListResources {
			lists[list.Addr().String()] = list
		}
		ctx.stepListsByStep[name] = lists

		outputs := make(map[string]*configs.Output, len(step.Outputs))
		for _, output := range step.Outputs {
			outputs[output.Name] = output
		}
		ctx.stepOutputsByStep[name] = outputs

		for _, execution := range step.Executions {
			for _, action := range execution.InvokeAction {
				if _, ok := action.(runbookaddrs.WorkspaceActionInstance); ok {
					ctx.usedWorkspaceActions = append(ctx.usedWorkspaceActions, action)
				}
			}
		}
	}

	return ctx, diags
}

func (c *RunbookContext) Config() *runbookconfig.RunbookConfig {
	if c == nil {
		return nil
	}
	return c.config
}

func (c *RunbookContext) Variable(name string) *configs.Variable {
	if c == nil {
		return nil
	}
	return c.variablesByName[name]
}

func (c *RunbookContext) Output(name string) *configs.Output {
	if c == nil {
		return nil
	}
	return c.outputsByName[name]
}

func (c *RunbookContext) Step(name string) *runbookconfig.Step {
	if c == nil {
		return nil
	}
	return c.stepsByName[name]
}

func (c *RunbookContext) StepLocal(stepName, localName string) *configs.Local {
	if c == nil {
		return nil
	}
	locals := c.stepLocalsByStep[stepName]
	if locals == nil {
		return nil
	}
	return locals[localName]
}

func (c *RunbookContext) StepAction(stepName, actionType, actionName string) *configs.Action {
	if c == nil {
		return nil
	}
	actions := c.stepActionsByStep[stepName]
	if actions == nil {
		return nil
	}
	return actions["action."+actionType+"."+actionName]
}

func (c *RunbookContext) StepDataSource(stepName, sourceType, sourceName string) *configs.Resource {
	if c == nil {
		return nil
	}
	dataSources := c.stepDataSourcesByStep[stepName]
	if dataSources == nil {
		return nil
	}
	return dataSources["data."+sourceType+"."+sourceName]
}

func (c *RunbookContext) StepList(stepName, listType, listName string) *configs.Resource {
	if c == nil {
		return nil
	}
	lists := c.stepListsByStep[stepName]
	if lists == nil {
		return nil
	}
	return lists["list."+listType+"."+listName]
}

func (c *RunbookContext) StepOutput(stepName, outputName string) *configs.Output {
	if c == nil {
		return nil
	}
	outputs := c.stepOutputsByStep[stepName]
	if outputs == nil {
		return nil
	}
	return outputs[outputName]
}

func (c *RunbookContext) UsedWorkspaceActions() []runbookaddrs.ExecutableAction {
	if c == nil {
		return nil
	}
	return c.usedWorkspaceActions
}

func (c *RunbookContext) UsedWorkspaceOutputs() []runbookaddrs.WorkspaceOutputValue {
	if c == nil {
		return nil
	}
	return c.usedWorkspaceOutputNames
}

func (c *RunbookContext) Validate() hcl.Diagnostics {
	var diags hcl.Diagnostics
	if c == nil || c.config == nil {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid runbook context",
			Detail:   "A runbook runtime context requires a non-nil runbook configuration.",
		})
		return diags
	}

	for _, step := range c.config.Steps {
		for _, execution := range step.Executions {
			for _, action := range execution.InvokeAction {
				diags = append(diags, c.validateExecutableAction(step.Name, action)...)
			}
		}

		for _, output := range step.Outputs {
			diags = append(diags, c.validateExpressionExternalReferences(output.Expr)...)
		}

		for _, condition := range step.Preconditions {
			diags = append(diags, c.validateExpressionExternalReferences(condition.Condition)...)
			diags = append(diags, c.validateExpressionExternalReferences(condition.ErrorMessage)...)
		}
		for _, condition := range step.Postconditions {
			diags = append(diags, c.validateExpressionExternalReferences(condition.Condition)...)
			diags = append(diags, c.validateExpressionExternalReferences(condition.ErrorMessage)...)
		}
	}

	for _, output := range c.config.Outputs {
		diags = append(diags, c.validateExpressionExternalReferences(output.Expr)...)
	}

	return diags
}

func (c *RunbookContext) validateExecutableAction(stepName string, action runbookaddrs.ExecutableAction) hcl.Diagnostics {
	var diags hcl.Diagnostics

	switch addr := action.(type) {
	case runbookaddrs.ActionInstance:
		if c.StepAction(stepName, addr.Type, addr.Name) == nil {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Reference to undeclared action",
				Detail:   fmt.Sprintf("The action %q is not declared in step %q.", addr.String(), stepName),
			})
		}
	case runbookaddrs.WorkspaceActionInstance:
		if !c.workspaceActionExists(addr.Action) {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Reference to undeclared workspace action",
				Detail:   fmt.Sprintf("The workspace action %q does not exist in the loaded Terraform configuration.", addr.String()),
			})
		}
	default:
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid execute target",
			Detail:   fmt.Sprintf("%T is not a valid executable action target.", action),
		})
	}

	return diags
}

func (c *RunbookContext) workspaceActionExists(addr addrs.AbsAction) bool {
	if c == nil || c.config == nil || c.config.WorkspaceConfig == nil {
		return false
	}

	moduleCfg := c.config.WorkspaceConfig.DescendantForInstance(addr.Module)
	if moduleCfg == nil || moduleCfg.Module == nil {
		return false
	}
	_, exists := moduleCfg.Module.Actions[addr.Action.String()]
	return exists
}

func (c *RunbookContext) validateExpressionExternalReferences(expr hcl.Expression) hcl.Diagnostics {
	var diags hcl.Diagnostics
	if expr == nil {
		return diags
	}

	for _, traversal := range expr.Variables() {
		rootName := traversal.RootName()
		if rootName != "step" && rootName != "workspace" {
			continue
		}

		ref, _, refDiags := runbookaddrs.ParseStepExternalReference(traversal)
		diags = append(diags, refDiags.ToHCL()...)
		if refDiags.HasErrors() {
			continue
		}

		switch addr := ref.Target.(type) {
		case runbookaddrs.StepOutputValue:
			cfgAddr := addr.ConfigStepOutputValue()
			if c.StepOutput(cfgAddr.Step.Name, cfgAddr.Name) == nil {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Reference to undeclared step output",
					Detail:   fmt.Sprintf("The step output %q does not exist in the loaded runbook configuration.", addr.String()),
					Subject:  traversal.SourceRange().Ptr(),
				})
			}
		case runbookaddrs.WorkspaceOutputValue:
			if !c.workspaceOutputExists(addr) {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Reference to undeclared workspace output",
					Detail:   fmt.Sprintf("The workspace output %q does not exist in the loaded Terraform configuration.", addr.String()),
					Subject:  traversal.SourceRange().Ptr(),
				})
				continue
			}
			c.usedWorkspaceOutputNames = append(c.usedWorkspaceOutputNames, addr)
		}
	}

	return diags
}

func (c *RunbookContext) workspaceOutputExists(addr runbookaddrs.WorkspaceOutputValue) bool {
	if c == nil || c.config == nil || c.config.WorkspaceConfig == nil {
		return false
	}

	if !addr.Output.Module.IsRoot() {
		return false
	}
	_, exists := c.config.WorkspaceConfig.Module.Outputs[addr.Output.OutputValue.Name]
	return exists
}
