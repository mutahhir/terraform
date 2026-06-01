package runbookconfigs

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/configs"
)

// Catch represents a catch block within a runbook configuration.
// Catch blocks are error handlers that execute when a step fails.
// Multiple catch blocks can coexist using preconditions to filter
// which failures they handle.
type Catch struct {
	Name          string
	Preconditions []*Condition
	Actions       []*configs.Action
	DataSources   []*configs.Resource
	Executions    []*Execution
	Locals        []*configs.Local
	Outputs       []*configs.Output
	DeclRange     hcl.Range
}

func decodeCatchBlock(block *hcl.Block) (*Catch, hcl.Diagnostics) {
	catch := &Catch{
		Name:          block.Labels[0],
		DeclRange:     block.DefRange,
		Actions:       []*configs.Action{},
		DataSources:   []*configs.Resource{},
		Executions:    []*Execution{},
		Preconditions: []*Condition{},
		Locals:        []*configs.Local{},
		Outputs:       []*configs.Output{},
	}

	var diags hcl.Diagnostics

	content, contentDiags := block.Body.Content(catchBlockSchema)
	diags = append(diags, contentDiags...)

	actionBlockTypes := make(map[string]map[string]hcl.Range)
	dataBlockTypes := make(map[string]map[string]hcl.Range)
	localNames := make(map[string]hcl.Range)
	outputNames := make(map[string]hcl.Range)

	for _, innerBlock := range content.Blocks {
		switch innerBlock.Type {
		case "precondition":
			cfg, cfgDiags := decodeConditionBlock(innerBlock)
			diags = append(diags, cfgDiags...)
			if cfg != nil {
				catch.Preconditions = append(catch.Preconditions, cfg)
			}

		case "action":
			cfg, cfgDiags := configs.DecodeActionBlock(innerBlock)
			diags = append(diags, cfgDiags...)
			if cfg == nil {
				continue
			}

			if _, exists := actionBlockTypes[cfg.Type]; !exists {
				actionBlockTypes[cfg.Type] = make(map[string]hcl.Range)
			}
			if rng, exists := actionBlockTypes[cfg.Type][cfg.Name]; exists {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate \"action\" block names",
					Detail:   "This catch block already has an action block named " + cfg.Type + "." + cfg.Name + " defined at " + rng.String(),
					Subject:  innerBlock.DefRange.Ptr(),
				})
				continue
			}

			actionBlockTypes[cfg.Type][cfg.Name] = cfg.DeclRange
			catch.Actions = append(catch.Actions, cfg)

		case "data":
			cfg, cfgDiags := configs.DecodeDataBlock(innerBlock, false, false)
			diags = append(diags, cfgDiags...)
			if cfg == nil {
				continue
			}

			if _, exists := dataBlockTypes[cfg.Type]; !exists {
				dataBlockTypes[cfg.Type] = make(map[string]hcl.Range)
			}
			if rng, exists := dataBlockTypes[cfg.Type][cfg.Name]; exists {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate \"data\" block names",
					Detail:   "This catch block already has a data block named " + cfg.Type + "." + cfg.Name + " defined at " + rng.String(),
					Subject:  innerBlock.DefRange.Ptr(),
				})
				continue
			}

			dataBlockTypes[cfg.Type][cfg.Name] = cfg.DeclRange
			catch.DataSources = append(catch.DataSources, cfg)

		case "execute":
			cfg, cfgDiags := decodeExecutionBlock(innerBlock)
			diags = append(diags, cfgDiags...)
			if cfg != nil {
				catch.Executions = append(catch.Executions, cfg)
			}

		case "locals":
			locals, cfgDiags := configs.DecodeLocalsBlock(innerBlock)
			diags = append(diags, cfgDiags...)
			for _, local := range locals {
				if rng, exists := localNames[local.Name]; exists {
					diags = append(diags, &hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Duplicate local value declaration",
						Detail:   "This catch block already has a local value named " + local.Name + " defined at " + rng.String() + ".",
						Subject:  local.DeclRange.Ptr(),
					})
					continue
				}
				localNames[local.Name] = local.DeclRange
				catch.Locals = append(catch.Locals, local)
			}

		case "output":
			cfg, cfgDiags := configs.DecodeOutputBlock(innerBlock, false)
			diags = append(diags, cfgDiags...)
			if cfg == nil {
				continue
			}

			if rng, exists := outputNames[cfg.Name]; exists {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate output declaration",
					Detail:   "This catch block already has an output named " + cfg.Name + " defined at " + rng.String() + ".",
					Subject:  innerBlock.DefRange.Ptr(),
				})
				continue
			}

			outputNames[cfg.Name] = cfg.DeclRange
			catch.Outputs = append(catch.Outputs, cfg)
		}
	}

	return catch, diags
}

var catchBlockSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "precondition"},
		{Type: "action", LabelNames: []string{"type", "name"}},
		{Type: "data", LabelNames: []string{"type", "name"}},
		{Type: "execute"},
		{Type: "locals"},
		{Type: "output", LabelNames: []string{"name"}},
	},
}
