package views

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/repl"
	runbookgraph "github.com/hashicorp/terraform/internal/runbooks/graph"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

type RunbookPlan interface {
	UI() runbookgraph.UI
	Hooks() []runbookgraph.Hook
	Diagnostics(tfdiags.Diagnostics)
	HelpPrompt()
	Plan(*runbookgraph.Plan)
}

type runbookPlan struct {
	Steps []runbookPlanStep `json:"steps"`
	Info  []runbookPlanInfo `json:"info"`
}

type runbookPlanStep struct {
	Name        string                     `json:"name"`
	Index       int                        `json:"index"`
	InstanceKey string                     `json:"instance_key,omitempty"`
	Status      runbookruntime.StepStatus  `json:"status"`
	SkipReason  string                     `json:"skip_reason,omitempty"`
	Outputs     map[string]json.RawMessage `json:"outputs,omitempty"`
}

type runbookPlanInfo struct {
	StepName  string                    `json:"step_name"`
	StepIndex int                       `json:"step_index"`
	Type      string                    `json:"type"`
	Subject   string                    `json:"subject"`
	Status    runbookruntime.StepStatus `json:"status"`
	Value     json.RawMessage           `json:"value,omitempty"`
	valueVal  cty.Value                 `json:"-"`
}

func NewRunbookPlan(vt arguments.ViewType, view *View) RunbookPlan {
	switch vt {
	case arguments.ViewJSON:
		return &RunbookPlanJSON{view: NewJSONView(view)}
	case arguments.ViewHuman:
		return &RunbookPlanHuman{view: view}
	default:
		panic(fmt.Sprintf("unknown view type %v", vt))
	}
}

type RunbookPlanHuman struct {
	view *View
	info []runbookPlanInfo
}

func (v *RunbookPlanHuman) UI() runbookgraph.UI                   { return v }
func (v *RunbookPlanHuman) Hooks() []runbookgraph.Hook            { return []runbookgraph.Hook{v} }
func (v *RunbookPlanHuman) Diagnostics(diags tfdiags.Diagnostics) { v.view.Diagnostics(diags) }
func (v *RunbookPlanHuman) HelpPrompt()                           { v.view.HelpPrompt("runbook plan") }
func (v *RunbookPlanHuman) PlannedStep(step *runbookruntime.Step) {}
func (v *RunbookPlanHuman) PlannedStepInfo(info runbookgraph.StepPlanInfo) {
	entry := runbookPlanInfo{
		StepName:  info.StepName,
		StepIndex: info.StepIndex,
		Type:      info.Type,
		Subject:   info.Subject,
		Status:    info.Status,
		valueVal:  info.Value,
	}
	if info.Value != cty.NilVal {
		if encoded, err := json.Marshal(tfdiags.CompactValueStr(info.Value)); err == nil {
			entry.Value = encoded
		}
	}
	v.info = append(v.info, entry)
}
func (v *RunbookPlanHuman) Plan(plan *runbookgraph.Plan) {
	viewPlan := buildRunbookPlan(plan, v.info)
	v.view.streams.Println("Terraform used the selected providers to generate the following runbook")
	v.view.streams.Println("plan. Runbook operations are indicated with the following symbols:")
	v.view.streams.Println("  <= read")
	v.view.streams.Println("  <> list")
	v.view.streams.Println("  > execute")
	v.view.streams.Println("")
	if len(viewPlan.Steps) == 0 {
		v.view.streams.Println("No changes. The runbook has no planned steps.")
		return
	}
	v.view.streams.Println("Terraform will perform the following runbook steps:")
	v.view.streams.Println("")

	infoByStep := make(map[string][]runbookPlanInfo)
	for _, info := range viewPlan.Info {
		key := fmt.Sprintf("%s[%d]", info.StepName, info.StepIndex)
		infoByStep[key] = append(infoByStep[key], info)
	}

	reads, lists, executes := 0, 0, 0
	for _, step := range viewPlan.Steps {
		stepInfo := dedupePlanInfo(infoByStep[fmt.Sprintf("%s[%d]", step.Name, step.Index)])
		v.view.streams.Println(renderRunbookStepPlan(step, stepInfo))
		for _, info := range stepInfo {
			switch info.Type {
			case "data":
				reads++
			case "list":
				lists++
			case "execute":
				executes++
			}
		}
	}
	v.view.streams.Println(fmt.Sprintf("Plan: %d to run, %d to skip. Operations: %d to read, %d to list, %d to execute.", countRunnableSteps(viewPlan.Steps), countSkippedSteps(viewPlan.Steps), reads, lists, executes))
}

type RunbookPlanJSON struct {
	view *JSONView
	info []runbookPlanInfo
}

func (v *RunbookPlanJSON) UI() runbookgraph.UI                   { return v }
func (v *RunbookPlanJSON) Hooks() []runbookgraph.Hook            { return []runbookgraph.Hook{v} }
func (v *RunbookPlanJSON) Diagnostics(diags tfdiags.Diagnostics) { v.view.Diagnostics(diags) }
func (v *RunbookPlanJSON) HelpPrompt()                           {}
func (v *RunbookPlanJSON) PlannedStep(step *runbookruntime.Step) {}
func (v *RunbookPlanJSON) PlannedStepInfo(info runbookgraph.StepPlanInfo) {
	entry := runbookPlanInfo{
		StepName:  info.StepName,
		StepIndex: info.StepIndex,
		Type:      info.Type,
		Subject:   info.Subject,
		Status:    info.Status,
		valueVal:  info.Value,
	}
	if info.Value != cty.NilVal {
		if encoded, err := json.Marshal(tfdiags.CompactValueStr(info.Value)); err == nil {
			entry.Value = encoded
		}
	}
	v.info = append(v.info, entry)
}
func (v *RunbookPlanJSON) Plan(plan *runbookgraph.Plan) {
	v.view.log.Info("Runbook plan", "type", "runbook_plan", "plan", buildRunbookPlan(plan, v.info))
}

func buildRunbookPlan(plan *runbookgraph.Plan, info []runbookPlanInfo) runbookPlan {
	ret := runbookPlan{Info: append([]runbookPlanInfo(nil), info...)}
	if plan == nil {
		return ret
	}
	ret.Steps = make([]runbookPlanStep, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		ret.Steps = append(ret.Steps, toRunbookPlanStep(step))
	}
	return ret
}

func toRunbookPlanStep(step *runbookruntime.Step) runbookPlanStep {
	ret := runbookPlanStep{
		Name:       step.Name,
		Index:      step.Index,
		Status:     step.Status,
		SkipReason: step.SkipReason,
	}
	if step.InstanceKey != nil {
		ret.InstanceKey = step.InstanceKey.String()
	}
	if step.Outputs != cty.NilVal && step.Outputs.IsKnown() && !step.Outputs.IsNull() && step.Outputs.Type().IsObjectType() {
		ret.Outputs = make(map[string]json.RawMessage)
		for name, value := range step.Outputs.AsValueMap() {
			encoded, err := json.Marshal(tfdiags.CompactValueStr(value))
			if err == nil {
				ret.Outputs[name] = encoded
			}
		}
	}
	return ret
}

func renderRunbookStepPlan(step runbookPlanStep, info []runbookPlanInfo) string {
	var b strings.Builder
	stepIndent := 2
	itemIndent := stepIndent + 4
	b.WriteString(fmt.Sprintf("%s# %s will be %s", indent(stepIndent), renderStepAddress(step), renderStepOutcome(step)))
	if step.SkipReason != "" {
		b.WriteString(fmt.Sprintf(" (%s)", step.SkipReason))
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("%sstep %q {\n", indent(stepIndent), step.Name))
	for _, item := range sortedPlanInfo(info) {
		if !shouldRenderPlanInfo(item) {
			continue
		}
		b.WriteString(renderPlanInfoLine(item, itemIndent))
	}
	b.WriteString(fmt.Sprintf("%s}\n", indent(stepIndent)))
	return b.String()
}

func renderStepAddress(step runbookPlanStep) string {
	if step.InstanceKey == "" {
		return fmt.Sprintf("step.%s", step.Name)
	}
	return fmt.Sprintf("step.%s%s", step.Name, step.InstanceKey)
}

func renderStepOutcome(step runbookPlanStep) string {
	switch step.Status {
	case runbookruntime.StepStatusSkipped:
		return "skipped"
	default:
		return "planned"
	}
}

func runbookPlanSymbol(typ string) string {
	switch typ {
	case "data":
		return "<="
	case "list":
		return "<>"
	case "execute":
		return ">"
	default:
		return "~"
	}
}

func renderPlanInfo(info runbookPlanInfo, indentSize int) string {
	switch info.Type {
	case "data":
		return fmt.Sprintf("data %q", info.Subject)
	case "list":
		return fmt.Sprintf("list %q", info.Subject)
	case "execute":
		if info.valueVal != cty.NilVal && info.valueVal.IsKnown() && !info.valueVal.IsNull() {
			return fmt.Sprintf("execute %q with %s", info.Subject, repl.FormatValue(pruneUnsetValue(info.valueVal), 0))
		}
		return fmt.Sprintf("execute %q", info.Subject)
	default:
		return fmt.Sprintf("%s %q", info.Type, info.Subject)
	}
}

func renderPlanInfoLine(info runbookPlanInfo, indentSize int) string {
	rendered := renderPlanInfo(info, indentSize)
	lines := strings.Split(rendered, "\n")
	if len(lines) == 1 {
		return fmt.Sprintf("%s%s %s\n", indent(indentSize), runbookPlanSymbol(info.Type), rendered)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s%s %s\n", indent(indentSize), runbookPlanSymbol(info.Type), lines[0]))
	for _, line := range lines[1:] {
		b.WriteString(fmt.Sprintf("%s%s\n", indent(indentSize+2), line))
	}
	return b.String()
}

func indent(spaces int) string {
	if spaces <= 0 {
		return ""
	}
	return strings.Repeat(" ", spaces)
}

func pruneUnsetValue(v cty.Value) cty.Value {
	if v == cty.NilVal || !v.IsKnown() || v.IsNull() {
		return v
	}
	ty := v.Type()
	switch {
	case ty.IsObjectType():
		attrs := make(map[string]cty.Value)
		for name, value := range v.AsValueMap() {
			if value.IsKnown() && value.IsNull() {
				continue
			}
			attrs[name] = pruneUnsetValue(value)
		}
		return cty.ObjectVal(attrs)
	case ty.IsMapType():
		attrs := make(map[string]cty.Value)
		for name, value := range v.AsValueMap() {
			if value.IsKnown() && value.IsNull() {
				continue
			}
			attrs[name] = pruneUnsetValue(value)
		}
		return cty.MapVal(attrs)
	case ty.IsTupleType(), ty.IsListType(), ty.IsSetType():
		vals := make([]cty.Value, 0, v.LengthInt())
		for it := v.ElementIterator(); it.Next(); {
			_, elem := it.Element()
			vals = append(vals, pruneUnsetValue(elem))
		}
		switch {
		case ty.IsTupleType():
			return cty.TupleVal(vals)
		case ty.IsListType():
			return cty.ListVal(vals)
		default:
			return cty.SetVal(vals)
		}
	default:
		return v
	}
}

func shouldRenderPlanInfo(info runbookPlanInfo) bool {
	switch info.Type {
	case "data", "list", "execute":
		return true
	default:
		return false
	}
}

func sortedPlanInfo(info []runbookPlanInfo) []runbookPlanInfo {
	ret := append([]runbookPlanInfo(nil), info...)
	sort.SliceStable(ret, func(i, j int) bool {
		if ret[i].Type == ret[j].Type {
			return ret[i].Subject < ret[j].Subject
		}
		return ret[i].Type < ret[j].Type
	})
	return ret
}

func dedupePlanInfo(info []runbookPlanInfo) []runbookPlanInfo {
	if len(info) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(info))
	ret := make([]runbookPlanInfo, 0, len(info))
	for _, item := range info {
		key := fmt.Sprintf("%s|%d|%s|%s|%s", item.StepName, item.StepIndex, item.Type, item.Subject, item.Status)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ret = append(ret, item)
	}
	return ret
}

func countRunnableSteps(steps []runbookPlanStep) int {
	count := 0
	for _, step := range steps {
		if step.Status != runbookruntime.StepStatusSkipped {
			count++
		}
	}
	return count
}

func countSkippedSteps(steps []runbookPlanStep) int {
	count := 0
	for _, step := range steps {
		if step.Status == runbookruntime.StepStatusSkipped {
			count++
		}
	}
	return count
}
