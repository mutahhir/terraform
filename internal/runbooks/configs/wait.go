package runbookconfigs

import (
	"github.com/hashicorp/hcl/v2"
)

// WaitMode distinguishes blind sleeps from polling waits.
type WaitMode string

const (
	WaitModeDuration WaitMode = "duration"
	WaitModePolling  WaitMode = "polling"
)

// Wait represents a named wait operation inside an execute block.
// Duration, timeout, interval, and max_attempts are stored as expressions
// so they can reference variables. They are evaluated at execution time.
type Wait struct {
	Name      string
	Mode      WaitMode
	DeclRange hcl.Range

	// Blind mode
	Duration hcl.Expression

	// Polling mode
	DataSource       hcl.Traversal
	Condition        hcl.Expression
	Timeout          hcl.Expression
	MaxAttempts      hcl.Expression
	Interval         hcl.Expression
	IgnoreReadErrors bool
}

func decodeWaitBlock(block *hcl.Block) (*Wait, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	wait := &Wait{
		Name:      block.Labels[0],
		DeclRange: block.DefRange,
	}

	content, contentDiags := block.Body.Content(waitSchema)
	diags = append(diags, contentDiags...)

	// Collect which attributes are present (for mode detection)
	var hasDuration, hasDataSource, hasCondition, hasTimeout, hasMaxAttempts, hasInterval bool

	if attr, exists := content.Attributes["duration"]; exists {
		hasDuration = true
		wait.Duration = attr.Expr
	}

	if attr, exists := content.Attributes["datasource"]; exists {
		hasDataSource = true
		traversal, travDiags := hcl.AbsTraversalForExpr(attr.Expr)
		diags = append(diags, travDiags...)
		if !travDiags.HasErrors() {
			wait.DataSource = traversal
		}
	}

	if attr, exists := content.Attributes["condition"]; exists {
		hasCondition = true
		wait.Condition = attr.Expr
	}

	if attr, exists := content.Attributes["timeout"]; exists {
		hasTimeout = true
		wait.Timeout = attr.Expr
	}

	if attr, exists := content.Attributes["max_attempts"]; exists {
		hasMaxAttempts = true
		wait.MaxAttempts = attr.Expr
	}

	if attr, exists := content.Attributes["interval"]; exists {
		hasInterval = true
		wait.Interval = attr.Expr
	}

	if attr, exists := content.Attributes["ignore_read_errors"]; exists {
		val, valDiags := attr.Expr.Value(nil)
		if !valDiags.HasErrors() && val.IsKnown() && !val.IsNull() {
			wait.IgnoreReadErrors = val.True()
		}
	}

	// Determine mode and validate mutual exclusivity
	hasPolling := hasDataSource || hasCondition || hasTimeout || hasMaxAttempts || hasInterval

	if hasDuration && hasPolling {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Cannot combine duration with polling attributes",
			Detail:   "A wait block must use either 'duration' for a blind sleep, or 'datasource'/'condition'/'timeout'/'max_attempts'/'interval' for polling. These are mutually exclusive.",
			Subject:  block.DefRange.Ptr(),
		})
		return wait, diags
	}

	if hasDuration {
		wait.Mode = WaitModeDuration
	} else if hasPolling {
		wait.Mode = WaitModePolling
		if !hasDataSource {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Missing required attribute",
				Detail:   "A polling wait requires a 'datasource' attribute.",
				Subject:  block.DefRange.Ptr(),
			})
		}
		if !hasCondition {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Missing required attribute",
				Detail:   "A polling wait requires a 'condition' attribute.",
				Subject:  block.DefRange.Ptr(),
			})
		}
		if !hasTimeout && !hasMaxAttempts {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Missing polling limit",
				Detail:   "A polling wait must have at least one of 'timeout' or 'max_attempts' to prevent infinite loops.",
				Subject:  block.DefRange.Ptr(),
			})
		}
	} else {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Empty wait block",
			Detail:   "A wait block must specify either 'duration' (blind wait) or 'datasource' + 'condition' (polling wait).",
			Subject:  block.DefRange.Ptr(),
		})
	}

	return wait, diags
}

var waitSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "duration"},
		{Name: "datasource"},
		{Name: "condition"},
		{Name: "timeout"},
		{Name: "max_attempts"},
		{Name: "interval"},
		{Name: "ignore_read_errors"},
	},
}
