package views

import (
	"fmt"
	"strings"

	"github.com/hashicorp/terraform/internal/repl"
	runbookgraph "github.com/hashicorp/terraform/internal/runbooks/graph"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/mitchellh/colorstring"
	"github.com/zclconf/go-cty/cty"
)

// planLeftPad is the left margin for all plan output lines.
const planLeftPad = "  "

// renderRunbookPlanHumanV2 renders the new two-part plan view:
// 1. Execution graph (timeline with parallelism)
// 2. Step details (operations, conditions, outputs)
func renderRunbookPlanHumanV2(view *View, plan *runbookgraph.Plan, viewPlan runbookPlan, mode runbookPlanRenderMode) {
	sym := selectPlanSymbols(view)
	c := view.colorize

	if mode == runbookPlanRenderModeShow {
		view.streams.Println(c.Color(sym.ColorHeader + "Saved runbook plan" + sym.ColorReset))
	} else {
		view.streams.Println(c.Color(sym.ColorHeader + "Runbook plan" + sym.ColorReset))
	}
	view.streams.Println("")

	if len(viewPlan.Steps) == 0 {
		if mode == runbookPlanRenderModeShow {
			view.streams.Println(planLeftPad + "The saved runbook plan has no planned steps.")
		} else {
			view.streams.Println(planLeftPad + "No changes. The runbook has no planned steps.")
		}
		return
	}

	// Part 1: Execution graph
	view.streams.Println(c.Color(planLeftPad + sym.ColorHeader + "Execution order:" + sym.ColorReset))
	view.streams.Println("")
	renderExecutionGraph(view, plan, sym)
	view.streams.Println("")

	// Part 2: Step details
	view.streams.Println(c.Color(planLeftPad + sym.ColorHeader + "Step details:" + sym.ColorReset))
	view.streams.Println(planLeftPad + planSeparator(sym))

	infoByStep := make(map[string][]runbookPlanInfo)
	for _, info := range viewPlan.Info {
		key := fmt.Sprintf("%s[%d]", info.StepName, info.StepIndex)
		infoByStep[key] = append(infoByStep[key], info)
	}

	for _, step := range viewPlan.Steps {
		view.streams.Println("")
		stepInfo := dedupePlanInfo(infoByStep[fmt.Sprintf("%s[%d]", step.Name, step.Index)])
		view.streams.Print(renderStepDetailsV2(step, stepInfo, sym, c))
		view.streams.Println(planLeftPad + planSeparator(sym))
	}

	// Outputs section
	outputSteps := stepsWithOutputs(viewPlan.Steps)
	if len(outputSteps) > 0 {
		view.streams.Println("")
		view.streams.Println(c.Color(planLeftPad + sym.ColorHeader + "Outputs:" + sym.ColorReset))
		for _, step := range outputSteps {
			for _, name := range sortedOutputNames(step.outputValues) {
				view.streams.Println(fmt.Sprintf("%s    %s = %s", planLeftPad, name, step.outputValues[name]))
			}
		}
		view.streams.Println("")
		view.streams.Println(planLeftPad + planSeparator(sym))
	}

	// Error handlers section (catch blocks)
	if plan != nil && plan.Config != nil && len(plan.Config.Catches) > 0 {
		view.streams.Println("")
		view.streams.Println(c.Color(planLeftPad + sym.ColorHeader + "Error handlers:" + sym.ColorReset))
		view.streams.Println("")
		renderCatchBlocks(view, plan, sym)
		view.streams.Println(planLeftPad + planSeparator(sym))
	}

	// Summary line
	reads, lists, executes := countOperations(viewPlan, infoByStep)
	prefix := "Plan"
	if mode == runbookPlanRenderModeShow {
		prefix = "Saved plan"
	}
	view.streams.Println(renderPlanSummary(prefix, viewPlan.Steps, reads, lists, executes))
}

func selectPlanSymbols(view *View) runbookPlanSymbols {
	if view.colorize.Disable {
		return asciiPlanSymbols()
	}
	return unicodePlanSymbols()
}

func planSeparator(sym runbookPlanSymbols) string {
	if sym.Dot == "*" {
		// ASCII mode
		return "-------------------------------------------------------------"
	}
	return "─────────────────────────────────────────────────────────────"
}

// renderExecutionGraph renders the timeline-based execution graph.
// Pattern:
//   - "* step.name" or "● step.name" for execution points
//   - "| step.name" or "│ step.name" for parallel peers
//   - "|" or "│" for time gap between execution points
func renderExecutionGraph(view *View, plan *runbookgraph.Plan, sym runbookPlanSymbols) {
	c := view.colorize
	pad := planLeftPad

	if plan == nil || len(plan.ExecutionLayers) == 0 {
		// Fallback: render steps as sequential
		for _, step := range plan.Steps {
			if step.Status == runbookruntime.StepStatusSkipped {
				view.streams.Println(c.Color(fmt.Sprintf("%s%s%s %s (skipped)%s", pad, sym.ColorSkip, sym.DotSkip, renderStepAddrFromRuntime(step), sym.ColorReset)))
			} else {
				view.streams.Println(c.Color(fmt.Sprintf("%s%s%s %s%s", pad, sym.ColorStep, sym.Dot, renderStepAddrFromRuntime(step), sym.ColorReset)))
			}
			view.streams.Println(c.Color(fmt.Sprintf("%s%s%s%s", pad, sym.ColorDim, sym.Timeline, sym.ColorReset)))
		}
		view.streams.Println(c.Color(fmt.Sprintf("%s%s%s outputs%s", pad, sym.ColorOutput, sym.Dot, sym.ColorReset)))
		return
	}

	for _, layer := range plan.ExecutionLayers {
		for i, step := range layer.Steps {
			addr := renderLayerStepAddr(step)
			if i == 0 {
				if step.Skipped {
					view.streams.Println(c.Color(fmt.Sprintf("%s%s%s %s (skipped)%s", pad, sym.ColorSkip, sym.DotSkip, addr, sym.ColorReset)))
				} else {
					view.streams.Println(c.Color(fmt.Sprintf("%s%s%s %s%s", pad, sym.ColorStep, sym.Dot, addr, sym.ColorReset)))
				}
			} else {
				if step.Skipped {
					view.streams.Println(c.Color(fmt.Sprintf("%s%s%s %s (skipped)%s", pad, sym.ColorSkip, sym.ParSkip, addr, sym.ColorReset)))
				} else {
					view.streams.Println(c.Color(fmt.Sprintf("%s%s%s %s%s", pad, sym.ColorStep, sym.Parallel, addr, sym.ColorReset)))
				}
			}
		}
		view.streams.Println(c.Color(fmt.Sprintf("%s%s%s%s", pad, sym.ColorDim, sym.Timeline, sym.ColorReset)))
	}
	view.streams.Println(c.Color(fmt.Sprintf("%s%s%s outputs%s", pad, sym.ColorOutput, sym.Dot, sym.ColorReset)))
}

func renderLayerStepAddr(step runbookgraph.ExecutionLayerStep) string {
	if step.InstanceKey == "" {
		return fmt.Sprintf("step.%s", step.Name)
	}
	return fmt.Sprintf("step.%s%s", step.Name, step.InstanceKey)
}

func renderStepAddrFromRuntime(step *runbookruntime.Step) string {
	if step.InstanceKey == nil {
		return fmt.Sprintf("step.%s", step.Name)
	}
	return fmt.Sprintf("step.%s%s", step.Name, step.InstanceKey.String())
}

// renderStepDetailsV2 renders the detailed breakdown of a single step.
func renderStepDetailsV2(step runbookPlanStep, info []runbookPlanInfo, sym runbookPlanSymbols, c *colorstring.Colorize) string {
	var b strings.Builder
	pad := planLeftPad

	// Step header
	if step.Status == runbookruntime.StepStatusSkipped {
		b.WriteString(c.Color(fmt.Sprintf("%s%s%s %s (skipped)%s\n", pad, sym.ColorSkip, sym.DotSkip, renderStepAddress(step), sym.ColorReset)))
		if step.SkipReason != "" {
			b.WriteString(c.Color(fmt.Sprintf("%s  %sreason: %s%s\n", pad, sym.ColorDim, step.SkipReason, sym.ColorReset)))
		}
		b.WriteString("\n")
		return b.String()
	}
	b.WriteString(c.Color(fmt.Sprintf("%s%s%s %s%s will execute\n", pad, sym.ColorStep, sym.Dot, renderStepAddress(step), sym.ColorReset)))

	sections := classifyRunbookStepInfo(info)

	// Preconditions
	// TODO: extract precondition expressions from config once StepPlanInfo carries them
	// For now, conditions are not emitted as plan info yet.

	// Reads
	if len(sections.reads) != 0 {
		b.WriteString("\n")
		for _, item := range sections.reads {
			b.WriteString(c.Color(fmt.Sprintf("%s    %s%s read%s  %s\n", pad, sym.ColorRead, sym.Read, sym.ColorReset, renderInfoSubjectV2(item))))
			renderInfoDetailsV2(&b, item, pad, sym, c)
		}
	}

	// Lists
	if len(sections.lists) != 0 {
		for _, item := range sections.lists {
			b.WriteString(c.Color(fmt.Sprintf("%s    %s%s list%s  %s\n", pad, sym.ColorRead, sym.Read, sym.ColorReset, renderInfoSubjectV2(item))))
			renderInfoDetailsV2(&b, item, pad, sym, c)
		}
	}

	// Action invocations (from executions, not raw action declarations)
	if len(sections.executions) != 0 {
		b.WriteString("\n")
		for _, item := range sections.executions {
			b.WriteString(c.Color(fmt.Sprintf("%s    %s%s invoke%s %s\n", pad, sym.ColorAction, sym.Action, sym.ColorReset, item.Subject)))
			renderActionAttributes(&b, item.valueVal, len(pad)+9, sym, c)
		}
	} else if len(sections.actions) != 0 {
		// Fallback: if no explicit execute info, show actions with their planned config
		b.WriteString("\n")
		for _, item := range sections.actions {
			b.WriteString(c.Color(fmt.Sprintf("%s    %s%s invoke%s %s\n", pad, sym.ColorAction, sym.Action, sym.ColorReset, item.Subject)))
			renderActionAttributes(&b, item.valueVal, len(pad)+9, sym, c)
		}
	}

	// Postconditions
	// TODO: extract postcondition expressions from config once StepPlanInfo carries them

	// Waits
	if len(sections.waits) != 0 {
		b.WriteString("\n")
		for _, item := range sections.waits {
			b.WriteString(c.Color(fmt.Sprintf("%s    %s%s wait%s  %s\n", pad, sym.ColorCondition, sym.Condition, sym.ColorReset, item.Subject)))
			if item.details != nil {
				var meta []string
				if mode, ok := item.details["mode"].(string); ok && mode == "duration" {
					if d, ok := item.details["duration"].(string); ok {
						meta = append(meta, "duration: "+d)
					}
				} else {
					if d, ok := item.details["interval"].(string); ok {
						meta = append(meta, "interval: "+d)
					}
					if d, ok := item.details["timeout"].(string); ok {
						meta = append(meta, "timeout: "+d)
					}
					if n, ok := item.details["max_attempts"].(float64); ok {
						meta = append(meta, fmt.Sprintf("max_attempts: %d", int(n)))
					}
				}
				if len(meta) > 0 {
					b.WriteString(c.Color(fmt.Sprintf("%s         %s%s%s\n", pad, sym.ColorDim, strings.Join(meta, ", "), sym.ColorReset)))
				}
			}
		}
	}

	// Outputs
	if len(step.outputValues) != 0 {
		b.WriteString("\n")
		b.WriteString(c.Color(fmt.Sprintf("%s    %s%s outputs:%s\n", pad, sym.ColorOutput, sym.Output, sym.ColorReset)))
		for _, name := range sortedOutputNames(step.outputValues) {
			b.WriteString(c.Color(fmt.Sprintf("%s         %s%s = %s%s\n", pad, sym.ColorDim, name, step.outputValues[name], sym.ColorReset)))
		}
	}

	b.WriteString("\n")
	return b.String()
}

func renderInfoSubjectV2(info runbookPlanInfo) string {
	switch info.Type {
	case "data", "list", "wait":
		// info.Subject already includes the mode prefix (e.g. "data.aws_lambda_invocation.check")
		return info.Subject
	case "workspace_read":
		if info.details != nil {
			kind := "object"
			if rawKind, ok := info.details["kind"].(string); ok && rawKind != "" {
				kind = rawKind
			}
			return fmt.Sprintf("workspace.%s.%s", kind, info.Subject)
		}
		return fmt.Sprintf("workspace.%s", info.Subject)
	default:
		return info.Subject
	}
}

func renderInfoDetailsV2(b *strings.Builder, info runbookPlanInfo, pad string, sym runbookPlanSymbols, c *colorstring.Colorize) {
	if info.details == nil {
		return
	}
	// Show relevant filter/query attributes from details
	if attrs, ok := info.details["attributes"].([]any); ok && len(attrs) > 0 {
		parts := make([]string, 0, len(attrs))
		for _, raw := range attrs {
			if attr, ok := raw.(string); ok && attr != "" {
				parts = append(parts, attr)
			}
		}
		if len(parts) > 0 {
			b.WriteString(c.Color(fmt.Sprintf("%s         %sattributes: [%s]%s\n", pad, sym.ColorDim, strings.Join(parts, ", "), sym.ColorReset)))
		}
	}
}

// renderActionAttributes renders a cty.Value as aligned key = value pairs,
// similar to how `terraform plan` renders resource attributes.
// For objects, each attribute is rendered as:
//
//	name    = "value"
//	integer = 42
//	enabled = true
//
// Indentation is controlled by the indent parameter (number of spaces before each line).
func renderActionAttributes(b *strings.Builder, v cty.Value, indent int, sym runbookPlanSymbols, c *colorstring.Colorize) {
	if v == cty.NilVal || !v.IsKnown() || v.IsNull() {
		return
	}

	v = pruneUnsetValue(v)

	// If it's not an object, render the whole value as a single line
	if !v.Type().IsObjectType() {
		b.WriteString(c.Color(fmt.Sprintf("%s%s%s%s\n", strings.Repeat(" ", indent), sym.ColorDim, formatAttributeValue(v, indent), sym.ColorReset)))
		return
	}

	// Collect attribute names and compute max key length for alignment
	attrNames := make([]string, 0)
	for name := range v.Type().AttributeTypes() {
		attrVal := v.GetAttr(name)
		if attrVal.IsNull() {
			continue
		}
		attrNames = append(attrNames, name)
	}
	if len(attrNames) == 0 {
		return
	}

	// Sort for deterministic output
	sortStringSlice(attrNames)

	// Compute max name length for alignment
	maxLen := 0
	for _, name := range attrNames {
		if len(name) > maxLen {
			maxLen = len(name)
		}
	}

	// Render each attribute
	pad := strings.Repeat(" ", indent)
	for _, name := range attrNames {
		attrVal := v.GetAttr(name)
		valStr := formatAttributeValue(attrVal, indent+maxLen+3)
		spacing := strings.Repeat(" ", maxLen-len(name))
		b.WriteString(c.Color(fmt.Sprintf("%s%s%s%s = %s%s\n", pad, sym.ColorDim, name, spacing, valStr, sym.ColorReset)))
	}
}

// formatAttributeValue renders a single cty.Value in a human-friendly way
// suitable for plan output. Unlike repl.FormatValue, this:
//   - Does not quote object attribute keys (they're identifiers, not strings)
//   - Renders (known after apply) for unknown values
//   - Renders (sensitive) for sensitive values
//   - Handles nested objects with proper indentation
func formatAttributeValue(v cty.Value, indent int) string {
	if !v.IsKnown() {
		return "(known after apply)"
	}
	if v.IsNull() {
		return "null"
	}

	ty := v.Type()
	switch {
	case ty == cty.String:
		return fmt.Sprintf("%q", v.AsString())
	case ty == cty.Number:
		bf := v.AsBigFloat()
		return bf.Text('f', -1)
	case ty == cty.Bool:
		if v.True() {
			return "true"
		}
		return "false"
	case ty.IsObjectType():
		return formatNestedObject(v, indent)
	case ty.IsMapType():
		return formatNestedObject(v, indent)
	case ty.IsListType(), ty.IsTupleType(), ty.IsSetType():
		return formatSequence(v, indent)
	default:
		return repl.FormatValue(v, indent)
	}
}

func formatNestedObject(v cty.Value, indent int) string {
	var b strings.Builder
	b.WriteString("{")

	count := 0
	var names []string
	for it := v.ElementIterator(); it.Next(); {
		k, _ := it.Element()
		names = append(names, k.AsString())
	}
	sortStringSlice(names)

	// Compute max key length for alignment within nested object
	maxLen := 0
	for _, name := range names {
		if len(name) > maxLen {
			maxLen = len(name)
		}
	}

	for it := v.ElementIterator(); it.Next(); {
		k, val := it.Element()
		name := k.AsString()
		spacing := strings.Repeat(" ", maxLen-len(name))
		b.WriteString("\n")
		b.WriteString(strings.Repeat(" ", indent+2))
		b.WriteString(fmt.Sprintf("%s%s = %s", name, spacing, formatAttributeValue(val, indent+2+maxLen+3)))
		count++
	}
	if count > 0 {
		b.WriteString("\n")
		b.WriteString(strings.Repeat(" ", indent))
	}
	b.WriteString("}")
	return b.String()
}

func formatSequence(v cty.Value, indent int) string {
	var b strings.Builder
	b.WriteString("[")

	count := 0
	for it := v.ElementIterator(); it.Next(); {
		_, elem := it.Element()
		if count > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n")
		b.WriteString(strings.Repeat(" ", indent+2))
		b.WriteString(formatAttributeValue(elem, indent+2))
		count++
	}
	if count > 0 {
		b.WriteString(",")
		b.WriteString("\n")
		b.WriteString(strings.Repeat(" ", indent))
	}
	b.WriteString("]")
	return b.String()
}

func sortStringSlice(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func stepsWithOutputs(steps []runbookPlanStep) []runbookPlanStep {
	var ret []runbookPlanStep
	for _, step := range steps {
		if len(step.outputValues) > 0 {
			ret = append(ret, step)
		}
	}
	return ret
}

func countOperations(viewPlan runbookPlan, infoByStep map[string][]runbookPlanInfo) (reads, lists, executes int) {
	for _, step := range viewPlan.Steps {
		stepInfo := dedupePlanInfo(infoByStep[fmt.Sprintf("%s[%d]", step.Name, step.Index)])
		for _, info := range stepInfo {
			switch info.Type {
			case "data", "workspace_read":
				reads++
			case "list":
				lists++
			case "execute":
				executes++
			}
		}
	}
	return
}

func renderPlanSummary(prefix string, steps []runbookPlanStep, reads, lists, executes int) string {
	runnable := countRunnableSteps(steps)
	skipped := countSkippedSteps(steps)
	total := len(steps)

	var parts []string
	if reads > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", reads, pluralize("read", reads)))
	}
	if lists > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", lists, pluralize("list", lists)))
	}
	if executes > 0 {
		parts = append(parts, fmt.Sprintf("%d action %s", executes, pluralize("invocation", executes)))
	}

	summary := fmt.Sprintf("%s: %d %s", prefix, total, pluralize("step", total))
	if skipped > 0 {
		summary += fmt.Sprintf(" (%d will execute, %d skipped)", runnable, skipped)
	} else {
		summary += fmt.Sprintf(" (%d will execute)", runnable)
	}
	if len(parts) > 0 {
		summary += "\nOperations: " + strings.Join(parts, ", ")
	}
	return summary
}

func pluralize(word string, count int) string {
	if count == 1 {
		return word
	}
	return word + "s"
}

// renderCatchBlocks renders the error handlers section of plan output.
func renderCatchBlocks(view *View, plan *runbookgraph.Plan, sym runbookPlanSymbols) {
	if plan == nil || plan.Config == nil {
		return
	}

	c := view.colorize
	catches := runbookgraph.SortedCatchNames(plan.Config)

	for _, name := range catches {
		catch := plan.Config.Catches[name]
		if catch == nil {
			continue
		}

		// Catch name line
		view.streams.Println(c.Color(fmt.Sprintf("%s    [yellow]%s catch %q[reset]", planLeftPad, sym.Lightning, name)))

		// Trigger description
		trigger := "any step failure"
		if len(catch.Preconditions) > 0 {
			trigger = fmt.Sprintf("%d precondition(s)", len(catch.Preconditions))
		}
		view.streams.Println(fmt.Sprintf("%s        Triggers: %s", planLeftPad, trigger))

		// Actions summary
		var ops []string
		for _, action := range catch.Actions {
			ops = append(ops, fmt.Sprintf("action.%s.%s", action.Type, action.Name))
		}
		for _, exec := range catch.Executions {
			for _, op := range exec.Operations {
				switch op.Type {
				case "read":
					ops = append(ops, "read_datasource")
				case "invoke_action":
					// Already counted via actions
				case "wait":
					ops = append(ops, "wait")
				}
			}
		}
		if len(catch.Outputs) > 0 {
			ops = append(ops, fmt.Sprintf("%d output(s)", len(catch.Outputs)))
		}
		if len(ops) > 0 {
			view.streams.Println(fmt.Sprintf("%s        Actions:  %s", planLeftPad, strings.Join(ops, ", ")))
		}

		view.streams.Println("")
	}
}
