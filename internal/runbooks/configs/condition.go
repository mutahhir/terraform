package runbookconfigs

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
)

type ConditionKind string

const (
	PreconditionCondition  ConditionKind = "precondition"
	PostconditionCondition ConditionKind = "postcondition"
)

type ConditionOnFail string

const (
	ConditionOnFailError ConditionOnFail = "error"
	ConditionOnFailSkip  ConditionOnFail = "skip"
)

type Condition struct {
	Kind      ConditionKind
	OnFail    ConditionOnFail
	DeclRange hcl.Range

	Condition    hcl.Expression
	ErrorMessage hcl.Expression
}

func decodeConditionBlock(block *hcl.Block) (*Condition, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	cr := &Condition{
		DeclRange: block.DefRange,
		Kind:      ConditionKind(block.Type),
		OnFail:    ConditionOnFailError, // Default to error if not specified
		Condition: nil,
	}

	content, moreDiags := block.Body.Content(conditionSchema)
	diags = append(diags, moreDiags...)

	cr.Condition = content.Attributes["assert"].Expr

	if len(cr.Condition.Variables()) == 0 {
		// A condition expression that doesn't refer to any variable is
		// pointless, because its result would always be a constant.
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("Invalid %s expression", block.Type),
			Detail:   "The condition expression must refer to at least one object from elsewhere in the configuration, or else its result would not be checking anything.",
			Subject:  cr.Condition.Range().Ptr(),
		})
	}

	cr.ErrorMessage = content.Attributes["error_message"].Expr

	if attr, exists := content.Attributes["on_failure"]; exists {
		onFailVal, err := attr.Expr.Value(nil)
		if err != nil {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid on_failure value",
				Detail:   "The on_failure attribute must be a string with a value of either \"error\" or \"skip\".",
				Subject:  attr.Expr.Range().Ptr(),
			})
		} else {
			onFailStr := onFailVal.AsString()
			switch onFailStr {
			case string(ConditionOnFailError):
				cr.OnFail = ConditionOnFailError
			case string(ConditionOnFailSkip):
				cr.OnFail = ConditionOnFailSkip
			default:
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid on_failure value",
					Detail:   "The on_failure attribute must be a string with a value of either \"error\" or \"skip\".",
					Subject:  attr.Expr.Range().Ptr(),
				})
			}
		}
	}

	return cr, diags
}

var conditionSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{
			Name:     "condition",
			Required: true,
		},
		{
			Name:     "error_message",
			Required: true,
		},
		{
			Name: "on_failure",
		},
	},
}
