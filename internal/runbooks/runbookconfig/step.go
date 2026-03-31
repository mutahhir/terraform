package runbookconfig

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/configs"
)

type Step struct {
	Name    string
	Count   hcl.Expression
	ForEach hcl.Expression

	Actions        []*configs.Action
	DataSources    []*configs.Resource
	ListResources  []*configs.Resource
	Executions     []*Execution
	Preconditions  []*Condition
	Postconditions []*Condition
	Locals         []*configs.Local
	Outputs        []*configs.Output

	DeclRange hcl.Range
}

func decodeStepBlock(stepBlock *hcl.Block) (*Step, hcl.Diagnostics) {
	step := &Step{
		Name:      stepBlock.Labels[0],
		DeclRange: stepBlock.DefRange,

		Actions:        []*configs.Action{},
		DataSources:    []*configs.Resource{},
		ListResources:  []*configs.Resource{},
		Executions:     []*Execution{},
		Preconditions:  []*Condition{},
		Postconditions: []*Condition{},
		Locals:         []*configs.Local{},
		Outputs:        []*configs.Output{},
	}

	var diags hcl.Diagnostics

	// Maybe we will need to use partial content here too, but remains to be
	// seen
	content, contentDiags := stepBlock.Body.Content(stepBlockSchema)
	diags = append(diags, contentDiags...)

	if attr, exists := content.Attributes["count"]; exists {
		step.Count = attr.Expr
	}

	if attr, exists := content.Attributes["for_each"]; exists {
		step.ForEach = attr.Expr
		if step.Count != nil {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  `Invalid combination of "count" and "for_each"`,
				Detail:   `The "count" and "for_each" meta-arguments are mutually-exclusive, only one should be used to be explicit about the number of step instances to create.`,
				Subject:  &attr.NameRange,
			})
		}
	}

	listBlockTypes := make(map[string]map[string]hcl.Range)
	dataBlockTypes := make(map[string]map[string]hcl.Range)
	actionBlockTypes := make(map[string]map[string]hcl.Range)
	localNames := make(map[string]hcl.Range)
	outputNames := make(map[string]hcl.Range)

	for _, innerBlock := range content.Blocks {
		switch innerBlock.Type {
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
					Detail:   "This step already has a data block named " + cfg.Type + "." + cfg.Name + " defined at " + rng.String(),
					Subject:  innerBlock.DefRange.Ptr(),
				})
				continue
			}

			dataBlockTypes[cfg.Type][cfg.Name] = cfg.DeclRange
			step.DataSources = append(step.DataSources, cfg)
		case "list":
			list, listDiags := configs.DecodeQueryListBlock(innerBlock)
			diags = append(diags, listDiags...)
			if list == nil || listDiags.HasErrors() {
				continue
			}

			if _, exists := listBlockTypes[list.Type]; !exists {
				listBlockTypes[list.Type] = make(map[string]hcl.Range)
			}

			if rng, exists := listBlockTypes[list.Type][list.Name]; exists {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate \"list\" block names",
					Detail:   "This step already has a list block named " + list.Type + "." + list.Name + " defined at " + rng.String(),
					Subject:  innerBlock.DefRange.Ptr(),
				})
				continue
			}

			listBlockTypes[list.Type][list.Name] = list.DeclRange
			step.ListResources = append(step.ListResources, list)
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
					Detail:   "This step already has an action block named " + cfg.Type + "." + cfg.Name + " defined at " + rng.String(),
					Subject:  innerBlock.DefRange.Ptr(),
				})
				continue
			}

			actionBlockTypes[cfg.Type][cfg.Name] = cfg.DeclRange
			step.Actions = append(step.Actions, cfg)
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
					Detail:   "This step already has an output named " + cfg.Name + " defined at " + rng.String() + ".",
					Subject:  innerBlock.DefRange.Ptr(),
				})
				continue
			}

			outputNames[cfg.Name] = cfg.DeclRange
			step.Outputs = append(step.Outputs, cfg)
		case "locals":
			locals, cfgDiags := configs.DecodeLocalsBlock(innerBlock)
			diags = append(diags, cfgDiags...)
			if locals != nil {
				for _, local := range locals {
					if rng, exists := localNames[local.Name]; exists {
						diags = append(diags, &hcl.Diagnostic{
							Severity: hcl.DiagError,
							Summary:  "Duplicate local value declaration",
							Detail:   "This step already has a local value named " + local.Name + " defined at " + rng.String() + ".",
							Subject:  local.DeclRange.Ptr(),
						})
						continue
					}

					localNames[local.Name] = local.DeclRange
					step.Locals = append(step.Locals, local)
				}
			}
		case "execute":
			cfg, cfgDiags := decodeExecutionBlock(innerBlock)
			diags = append(diags, cfgDiags...)
			if cfg != nil {
				step.Executions = append(step.Executions, cfg)
			}
		// While these are similar to terraform's pre and postcondition blocks
		// they have an extra onFailure mode which we need to support separately
		case "precondition", "postcondition":
			cfg, cfgDiags := decodeConditionBlock(innerBlock)
			diags = append(diags, cfgDiags...)
			if cfg != nil {
				if innerBlock.Type == "precondition" {
					step.Preconditions = append(step.Preconditions, cfg)
				}
				if innerBlock.Type == "postcondition" {
					step.Postconditions = append(step.Postconditions, cfg)
				}
			}
		}
	}

	return step, diags
}

var stepBlockSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{{Name: "count"}, {Name: "for_each"}},
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "locals"},
		{Type: "action", LabelNames: []string{"type", "name"}},
		{Type: "data", LabelNames: []string{"type", "name"}},
		{Type: "list", LabelNames: []string{"type", "name"}},
		{Type: "execute"},
		{Type: "output", LabelNames: []string{"name"}},
		{Type: "precondition"},
		{Type: "postcondition"},
	},
}
