// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"maps"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/dag"
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
	stepVertices             map[string]stepVertex
	stepDependencyGraph      *dag.AcyclicGraph
	stepLocalsByStep         map[string]map[string]*configs.Local
	stepActionsByStep        map[string]map[string]*configs.Action
	stepDataSourcesByStep    map[string]map[string]*configs.Resource
	stepListsByStep          map[string]map[string]*configs.Resource
	stepOutputsByStep        map[string]map[string]*configs.Output
	workspaceActionsByStep   map[string][]runbookaddrs.WorkspaceActionInstance
	workspaceOutputsByStep   map[string][]runbookaddrs.WorkspaceOutputValue
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
		stepVertices:             make(map[string]stepVertex, len(opts.Config.Steps)),
		stepDependencyGraph:      &dag.AcyclicGraph{},
		stepLocalsByStep:         make(map[string]map[string]*configs.Local, len(opts.Config.Steps)),
		stepActionsByStep:        make(map[string]map[string]*configs.Action, len(opts.Config.Steps)),
		stepDataSourcesByStep:    make(map[string]map[string]*configs.Resource, len(opts.Config.Steps)),
		stepListsByStep:          make(map[string]map[string]*configs.Resource, len(opts.Config.Steps)),
		stepOutputsByStep:        make(map[string]map[string]*configs.Output, len(opts.Config.Steps)),
		workspaceActionsByStep:   make(map[string][]runbookaddrs.WorkspaceActionInstance, len(opts.Config.Steps)),
		workspaceOutputsByStep:   make(map[string][]runbookaddrs.WorkspaceOutputValue, len(opts.Config.Steps)),
		usedWorkspaceActions:     make([]runbookaddrs.ExecutableAction, 0),
		usedWorkspaceOutputNames: make([]runbookaddrs.WorkspaceOutputValue, 0),
	}

	maps.Copy(ctx.variablesByName, opts.Config.Variables)
	maps.Copy(ctx.outputsByName, opts.Config.Outputs)

	for name, step := range opts.Config.Steps {
		ctx.stepsByName[name] = step
		v := stepVertex{NameValue: name}
		ctx.stepVertices[name] = v
		ctx.stepDependencyGraph.Add(v)
		ctx.workspaceActionsByStep[name] = make([]runbookaddrs.WorkspaceActionInstance, 0)
		ctx.workspaceOutputsByStep[name] = make([]runbookaddrs.WorkspaceOutputValue, 0)

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
				if workspaceAction, ok := action.(runbookaddrs.WorkspaceActionInstance); ok {
					ctx.usedWorkspaceActions = append(ctx.usedWorkspaceActions, action)
					ctx.workspaceActionsByStep[name] = append(ctx.workspaceActionsByStep[name], workspaceAction)
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

func (c *RunbookContext) StepDependencies(stepName string) []*runbookconfig.Step {
	if c == nil {
		return nil
	}
	v, ok := c.stepVertices[stepName]
	if !ok || c.stepDependencyGraph == nil {
		return nil
	}
	deps := c.stepDependencyGraph.DownEdges(v)
	ret := make([]*runbookconfig.Step, 0, len(deps))
	for _, raw := range deps {
		dep, ok := raw.(stepVertex)
		if ok {
			if step := c.stepsByName[dep.Name()]; step != nil {
				ret = append(ret, step)
			}
		}
	}
	slices.SortFunc(ret, func(a, b *runbookconfig.Step) int {
		return strings.Compare(a.Name, b.Name)
	})
	return ret
}

func (c *RunbookContext) StepExecutionOrder() []*runbookconfig.Step {
	if c == nil {
		return nil
	}
	if c.stepDependencyGraph == nil || len(c.stepDependencyGraph.Cycles()) != 0 {
		return nil
	}

	ordered := c.stepDependencyGraph.ReverseTopologicalOrder()
	ret := make([]*runbookconfig.Step, 0, len(ordered))
	for _, raw := range ordered {
		step, ok := raw.(stepVertex)
		if !ok {
			continue
		}
		cfg := c.stepsByName[step.Name()]
		if cfg == nil {
			return nil
		}
		ret = append(ret, cfg)
	}
	if len(ret) != len(c.stepsByName) {
		return nil
	}

	return ret
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

func (c *RunbookContext) StepWorkspaceActions(stepName string) []runbookaddrs.WorkspaceActionInstance {
	if c == nil {
		return nil
	}
	return c.workspaceActionsByStep[stepName]
}

func (c *RunbookContext) UsedWorkspaceOutputs() []runbookaddrs.WorkspaceOutputValue {
	if c == nil {
		return nil
	}
	return c.usedWorkspaceOutputNames
}

func (c *RunbookContext) StepWorkspaceOutputs(stepName string) []runbookaddrs.WorkspaceOutputValue {
	if c == nil {
		return nil
	}
	return c.workspaceOutputsByStep[stepName]
}

type stepVertex struct {
	NameValue string
}

func (v stepVertex) Name() string {
	return v.NameValue
}
