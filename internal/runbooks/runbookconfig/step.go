package runbookconfig

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/configs"
)

type Step struct {
	Name string

	Actions        []*configs.Action
	DataSources    []*configs.Resource
	ListResources  []*configs.ListResource
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
		ListResources:  []*configs.ListResource{},
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

	listBlockTypes := make(map[string]map[string]hcl.Range)

	for _, innerBlock := range content.Blocks {
		switch innerBlock.Type {
		case "data":
			cfg, cfgDiags := configs.DecodeDataBlock(innerBlock, false, false)
			diags = append(diags, cfgDiags...)
			if cfg != nil {
				step.DataSources = append(step.DataSources, cfg)
			}
		case "list":
			list, listDiags := configs.DecodeQueryListBlock(innerBlock)
			diags = append(diags, listDiags...)
			if !listDiags.HasErrors() {
				step.ListResources = append(step.ListResources, list.List)
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
		case "action":
			cfg, cfgDiags := configs.DecodeActionBlock(innerBlock)
			diags = append(diags, cfgDiags...)
			if cfg != nil {
				step.Actions = append(step.Actions, cfg)
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
