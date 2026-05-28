package runbookconfigs

import (
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2"
)

// WaitMode distinguishes blind sleeps from polling waits.
type WaitMode string

const (
	WaitModeDuration WaitMode = "duration"
	WaitModePolling  WaitMode = "polling"
)

// Wait represents a named wait operation inside an execute block.
type Wait struct {
	Name      string
	Mode      WaitMode
	DeclRange hcl.Range

	// Blind mode
	Duration time.Duration

	// Polling mode
	DataSource  hcl.Traversal
	Condition   hcl.Expression
	Timeout     time.Duration
	MaxAttempts int
	Interval    time.Duration // default 10s
}

func decodeWaitBlock(block *hcl.Block) (*Wait, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	wait := &Wait{
		Name:      block.Labels[0],
		DeclRange: block.DefRange,
	}

	content, contentDiags := block.Body.Content(waitSchema)
	diags = append(diags, contentDiags...)

	// Parse duration (blind mode)
	var hasDuration bool
	if attr, exists := content.Attributes["duration"]; exists {
		hasDuration = true
		val, valDiags := attr.Expr.Value(nil)
		diags = append(diags, valDiags...)
		if !valDiags.HasErrors() {
			d, err := time.ParseDuration(val.AsString())
			if err != nil {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid duration value",
					Detail:   fmt.Sprintf("Could not parse duration: %s", err),
					Subject:  attr.Expr.Range().Ptr(),
				})
			} else {
				wait.Duration = d
			}
		}
	}

	// Parse polling attributes
	var hasDataSource, hasCondition bool
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
		val, valDiags := attr.Expr.Value(nil)
		diags = append(diags, valDiags...)
		if !valDiags.HasErrors() {
			d, err := time.ParseDuration(val.AsString())
			if err != nil {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid timeout value",
					Detail:   fmt.Sprintf("Could not parse timeout: %s", err),
					Subject:  attr.Expr.Range().Ptr(),
				})
			} else {
				wait.Timeout = d
			}
		}
	}

	if attr, exists := content.Attributes["max_attempts"]; exists {
		val, valDiags := attr.Expr.Value(nil)
		diags = append(diags, valDiags...)
		if !valDiags.HasErrors() {
			bf := val.AsBigFloat()
			n, _ := bf.Int64()
			wait.MaxAttempts = int(n)
		}
	}

	if attr, exists := content.Attributes["interval"]; exists {
		val, valDiags := attr.Expr.Value(nil)
		diags = append(diags, valDiags...)
		if !valDiags.HasErrors() {
			d, err := time.ParseDuration(val.AsString())
			if err != nil {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid interval value",
					Detail:   fmt.Sprintf("Could not parse interval: %s", err),
					Subject:  attr.Expr.Range().Ptr(),
				})
			} else {
				wait.Interval = d
			}
		}
	}

	// Determine mode and validate mutual exclusivity
	hasPolling := hasDataSource || hasCondition || wait.Timeout > 0 || wait.MaxAttempts > 0 || wait.Interval > 0

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
		if wait.Duration < time.Second {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Wait duration too short",
				Detail:   "Wait duration must be at least 1s.",
				Subject:  block.DefRange.Ptr(),
			})
		}
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
		if wait.Timeout == 0 && wait.MaxAttempts == 0 {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Missing polling limit",
				Detail:   "A polling wait must have at least one of 'timeout' or 'max_attempts' to prevent infinite loops.",
				Subject:  block.DefRange.Ptr(),
			})
		}
		if wait.Interval > 0 && wait.Interval < time.Second {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Wait interval too short",
				Detail:   "Wait interval must be at least 1s.",
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
	},
}
