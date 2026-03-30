// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package command

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/terraform/internal/plans"
	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/runbooks"
	"github.com/hashicorp/terraform/internal/runbooks/runbookplanfile"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/mitchellh/colorstring"
	"github.com/zclconf/go-cty/cty"
	ctymsgpack "github.com/zclconf/go-cty/cty/msgpack"
)

const (
	runbookPlanFilename = "runbook.tfrunplan"
)

func (c *RunbookCommand) runbookPlanPath() string {
	return filepath.Join(c.DataDir(), runbookPlanFilename)
}

func persistedStepFromPlan(stepName string, after []string, rawStepOutputs []string, planResp *runbooks.PlanRunbookStep_Response, rawActions, rawQueries []string, rawData []string) runbookplanfile.Step {
	ret := runbookplanfile.Step{
		Name:           stepName,
		After:          append([]string(nil), after...),
		PlannedActions: append([]string(nil), rawActions...),
		PlannedQueries: append([]string(nil), rawQueries...),
		PlannedData:    append([]string(nil), rawData...),
		Outputs:        append([]string(nil), rawStepOutputs...),
	}
	if planResp != nil {
		if planResp.GetStatus() == runbooks.StepStatus_STEP_STATUS_SKIPPED {
			ret.KnownSkipped = true
			if len(planResp.Diagnostics) > 0 {
				ret.SkipReason = planResp.Diagnostics[0].Detail
			}
		}
		if len(planResp.PlannedActions) > 0 {
			ret.PlannedActions = make([]string, 0, len(planResp.PlannedActions))
			for _, action := range planResp.PlannedActions {
				ret.PlannedActions = append(ret.PlannedActions, action.GetAddress())
			}
		}
		if len(planResp.PlannedQueries) > 0 {
			ret.PlannedQueries = make([]string, 0, len(planResp.PlannedQueries))
			for _, query := range planResp.PlannedQueries {
				ret.PlannedQueries = append(ret.PlannedQueries, query.GetAddress())
			}
		}
	}
	return ret
}

func persistedExpandedStep(baseName, instanceName, forEachKey string, instanceCount int, after []string, rawStepOutputs []string, rawActions, rawQueries, rawData []string) runbookplanfile.Step {
	return runbookplanfile.Step{
		Name:           instanceName,
		BaseName:       baseName,
		ForEachKey:     forEachKey,
		InstanceCount:  instanceCount,
		After:          append([]string(nil), after...),
		PlannedActions: append([]string(nil), rawActions...),
		PlannedQueries: append([]string(nil), rawQueries...),
		PlannedData:    append([]string(nil), rawData...),
		Outputs:        append([]string(nil), rawStepOutputs...),
	}
}

func formatPlanSummary(color *colorstring.Colorize, manifest *runbookplanfile.Plan) string {
	if manifest == nil {
		return ""
	}
	if color == nil {
		color = &colorstring.Colorize{Disable: true}
	}
	var b strings.Builder
	b.WriteString(color.Color("[bold]Runbook Execution Plan[reset]\n\n"))
	b.WriteString(color.Color("[cyan]Terraform will perform the following runbook steps:[reset]\n\n"))

	executableCount := 0
	skippedCount := 0
	for i, step := range manifest.Steps {
		if step.KnownSkipped {
			skippedCount++
		} else {
			executableCount++
		}
		verb := "will execute"
		prefix := color.Color("[cyan]~[reset]")
		if step.KnownSkipped {
			verb = "will be skipped"
			prefix = color.Color("[yellow]-[reset]")
		}
		b.WriteString(color.Color(fmt.Sprintf("[bold][cyan]# Step %d: %s %s[reset]\n", i+1, step.Name, verb)))
		b.WriteString(fmt.Sprintf("  %s %s {\n", prefix, colorizedStepRef(color, step.Name)))
		if step.SkipReason != "" {
			b.WriteString(fmt.Sprintf("      %s = %s\n", colorizedAttr(color, padRight("reason", 18)), colorizedFormattedValue(color, fmt.Sprintf("%q", step.SkipReason))))
		}
		sections := make([]string, 0, len(step.PlannedActionInfo)+len(step.PlannedDataInfo)+len(step.PlannedQueryInfo)+1)
		for _, info := range step.PlannedActionInfo {
			if rendered := strings.TrimRight(formatRunbookPlanActionBlock(info), "\n"); rendered != "" {
				sections = append(sections, rendered)
			}
		}
		for _, info := range step.PlannedDataInfo {
			if rendered := strings.TrimRight(formatRunbookPlanDataBlock(info), "\n"); rendered != "" {
				sections = append(sections, rendered)
			}
		}
		for _, info := range step.PlannedQueryInfo {
			if rendered := strings.TrimRight(formatRunbookPlanQueryBlock(info), "\n"); rendered != "" {
				sections = append(sections, rendered)
			}
		}
		if rendered := formatRunbookPlanOutputs(step.PlannedOutputs); rendered != "" {
			sections = append(sections, strings.TrimRight(rendered, "\n"))
		}
		if len(sections) > 0 {
			if step.SkipReason != "" {
				b.WriteString("\n")
			}
			b.WriteString(strings.Join(sections, "\n\n"))
			b.WriteString("\n")
		}
		b.WriteString("  }\n")
		if i < len(manifest.Steps)-1 {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(color.Color(fmt.Sprintf("[bold][green]Plan:[reset] %d to execute, %d to skip.\n", executableCount, skippedCount)))
	return b.String()
}

func formatRunbookPlanActionBlock(info runbookplanfile.PlannedActionInfo) string {
	if info.Address == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("      %s\n", colorizedComment(nil, fmt.Sprintf("# %s will invoke", info.Address))))
	b.WriteString(fmt.Sprintf("      %s %s {\n", colorizedKeyword(nil, "action"), colorizedBlockLabels(nil, info.Type, info.Name)))
	b.WriteString(formatRunbookPlanConfigAttrs(info.Config, "        "))
	b.WriteString("      }\n")
	return b.String()
}

func formatRunbookPlanDataBlock(info runbookplanfile.PlannedDataInfo) string {
	if info.Address == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("      %s\n", colorizedComment(nil, fmt.Sprintf("# %s will be read during execute", info.Address))))
	b.WriteString(fmt.Sprintf("      %s %s {\n", colorizedKeyword(nil, "data"), colorizedBlockLabels(nil, info.Type, info.Name)))
	b.WriteString(formatRunbookPlanConfigAttrs(info.Config, "        "))
	b.WriteString("      }\n")
	return b.String()
}

func formatRunbookPlanQueryBlock(info runbookplanfile.PlannedQueryInfo) string {
	if info.Address == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("      %s\n", colorizedComment(nil, fmt.Sprintf("# %s will query during execute", info.Address))))
	b.WriteString(fmt.Sprintf("      %s %s {\n", colorizedKeyword(nil, "list"), colorizedBlockLabels(nil, info.Type, info.Name)))
	b.WriteString(formatRunbookPlanConfigAttrs(info.Config, "        "))
	if info.Count > 0 {
		b.WriteString(fmt.Sprintf("        %s = %s\n", colorizedAttr(nil, padRight("result_count", 18)), colorizedFormattedValue(nil, fmt.Sprintf("%d", info.Count))))
	}
	b.WriteString("      }\n")
	return b.String()
}

func formatRunbookPlanOutputs(raw map[string][]byte) string {
	if len(raw) == 0 {
		return ""
	}
	keys := make([]string, 0, len(raw))
	for name := range raw {
		if strings.HasPrefix(name, "__runbook_") {
			continue
		}
		keys = append(keys, name)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("      %s = {\n", colorizedKeyword(nil, "outputs")))
	for _, name := range keys {
		b.WriteString(fmt.Sprintf("        %s = %s\n", colorizedAttr(nil, padRight(name, 18)), colorizedFormattedValue(nil, formatRunbookPlanValueBytes(raw[name]))))
	}
	b.WriteString("      }\n")
	return b.String()
}

func formatRunbookPlanConfigAttrs(raw map[string][]byte, indent string) string {
	if len(raw) == 0 {
		return ""
	}
	keys := make([]string, 0, len(raw))
	for name := range raw {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, name := range keys {
		formatted := formatRunbookPlanValueBytes(raw[name])
		if formatted == "null" {
			continue
		}
		b.WriteString(fmt.Sprintf("%s%s = %s\n", indent, colorizedAttr(nil, padRight(name, 18)), colorizedFormattedValue(nil, formatted)))
	}
	return b.String()
}

func formatRunbookPlanValueBytes(raw []byte) string {
	if len(raw) == 0 {
		return "null"
	}
	val, err := ctymsgpack.Unmarshal(raw, cty.DynamicPseudoType)
	if err != nil {
		return "(known after execute)"
	}
	if !val.IsKnown() {
		return "(known after execute)"
	}
	if val.IsNull() {
		return "null"
	}
	return tfdiags.CompactValueStr(val)
}

func quoteAndJoin(vals []string) string {
	quoted := make([]string, 0, len(vals))
	for _, val := range vals {
		quoted = append(quoted, fmt.Sprintf("%q", val))
	}
	return strings.Join(quoted, ", ")
}

func colorizedKeyword(color *colorstring.Colorize, value string) string {
	return colorized(color, "[cyan][bold]", value)
}

func colorizedAttr(color *colorstring.Colorize, value string) string {
	return colorized(color, "[white][bold]", value)
}

func colorizedComment(color *colorstring.Colorize, value string) string {
	return colorized(color, "[dark_gray]", value)
}

func colorizedStepRef(color *colorstring.Colorize, stepName string) string {
	return colorizedKeyword(color, "step") + "." + colorized(color, "[white][bold]", stepName)
}

func colorizedBlockLabels(color *colorstring.Colorize, typeName, name string) string {
	return colorizedString(color, typeName) + " " + colorizedString(color, name)
}

func colorizedString(color *colorstring.Colorize, value string) string {
	return colorized(color, "[green]", fmt.Sprintf("%q", value))
}

func colorizedFormattedValue(color *colorstring.Colorize, value string) string {
	style := "[green]"
	switch {
	case value == "null":
		style = "[dark_gray]"
	case strings.HasPrefix(value, "("):
		style = "[yellow]"
	case value == "true" || value == "false":
		style = "[green][bold]"
	case isNumericLiteral(value):
		style = "[green][bold]"
	}
	return colorized(color, style, value)
}

func colorized(color *colorstring.Colorize, style, value string) string {
	if color == nil {
		return value
	}
	return color.Color(style + value + "[reset]")
}

func isNumericLiteral(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if (r < '0' || r > '9') && r != '.' && !(i == 0 && r == '-') {
			return false
		}
	}
	return true
}

func padRight(value string, width int) string {
	if len(value) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-len(value))
}

func formatStepOutputs(color *colorstring.Colorize, stepName string, outputs cty.Value) string {
	if outputs == cty.NilVal || !outputs.IsKnown() || outputs.IsNull() || !outputs.Type().IsObjectType() {
		return ""
	}
	if color == nil {
		color = &colorstring.Colorize{Disable: true}
	}
	vals := outputs.AsValueMap()
	if len(vals) == 0 {
		return ""
	}
	keys := make([]string, 0, len(vals))
	for name := range vals {
		if strings.HasPrefix(name, "__runbook_") {
			continue
		}
		keys = append(keys, name)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(color.Color(fmt.Sprintf("[bold]%s outputs:[reset]\n", stepName)))
	for _, name := range keys {
		b.WriteString(fmt.Sprintf("  %s = %s\n", name, tfdiags.CompactValueStr(vals[name])))
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatStepExecutionPreview(color *colorstring.Colorize, step *runbookplanfile.Step, plannedOutputs map[string]cty.Value) string {
	if step == nil {
		return ""
	}
	if color == nil {
		color = &colorstring.Colorize{Disable: true}
	}
	var b strings.Builder
	b.WriteString(color.Color(fmt.Sprintf("[bold]Step Execution Preview: %s[reset]\n", step.Name)))
	if len(step.After) > 0 {
		b.WriteString(fmt.Sprintf("after = [%s]\n", strings.Join(step.After, ", ")))
	}
	if len(step.PlannedActions) > 0 {
		b.WriteString("actions = [\n")
		for _, action := range step.PlannedActions {
			b.WriteString(fmt.Sprintf("  %q,\n", action))
		}
		b.WriteString("]\n")
	}
	if len(step.PlannedQueries) > 0 {
		b.WriteString("queries = [\n")
		for _, query := range step.PlannedQueries {
			b.WriteString(fmt.Sprintf("  %q,\n", query))
		}
		b.WriteString("]\n")
	}
	if len(step.PlannedData) > 0 {
		b.WriteString("data_reads = [\n")
		for _, data := range step.PlannedData {
			b.WriteString(fmt.Sprintf("  %q,\n", data))
		}
		b.WriteString("]\n")
	}
	if len(plannedOutputs) > 0 {
		keys := make([]string, 0, len(plannedOutputs))
		for name := range plannedOutputs {
			if strings.HasPrefix(name, "__runbook_") {
				continue
			}
			keys = append(keys, name)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			b.WriteString("planned_outputs = {\n")
			for _, name := range keys {
				b.WriteString(fmt.Sprintf("  %s = %s\n", name, tfdiags.CompactValueStr(plannedOutputs[name])))
			}
			b.WriteString("}\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func plannedOutputValuesFromPlan(tfPlan *plans.Plan) map[string]cty.Value {
	if tfPlan == nil || tfPlan.Changes == nil {
		return nil
	}
	ret := map[string]cty.Value{}
	for _, output := range tfPlan.Changes.Outputs {
		if output == nil {
			continue
		}
		decoded, err := output.Decode()
		if err != nil {
			continue
		}
		ret[decoded.Addr.OutputValue.Name] = decoded.Change.After
	}
	if len(ret) == 0 {
		return nil
	}
	return ret
}

func copyLoweredFiles(planResp *runbooks.PlanRunbookStep_Response) map[string][]byte {
	ret := make(map[string][]byte)
	if planResp == nil {
		return ret
	}
	for _, file := range planResp.LoweredFiles {
		ret[file.Path] = append([]byte(nil), file.Content...)
	}
	return ret
}
