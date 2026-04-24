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
	ctyjson "github.com/zclconf/go-cty/cty/json"
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
	Name         string                     `json:"name"`
	Index        int                        `json:"index"`
	InstanceKey  string                     `json:"instance_key,omitempty"`
	Status       runbookruntime.StepStatus  `json:"status"`
	SkipReason   string                     `json:"skip_reason,omitempty"`
	Outputs      map[string]json.RawMessage `json:"outputs,omitempty"`
	outputValues map[string]string          `json:"-"`
}

type runbookPlanInfo struct {
	StepName  string                    `json:"step_name"`
	StepIndex int                       `json:"step_index"`
	Type      string                    `json:"type"`
	Subject   string                    `json:"subject"`
	Status    runbookruntime.StepStatus `json:"status"`
	Value     json.RawMessage           `json:"value,omitempty"`
	Details   json.RawMessage           `json:"details,omitempty"`
	details   map[string]any            `json:"-"`
	valueVal  cty.Value                 `json:"-"`
}

func NewRunbookPlan(vt arguments.ViewType, view *View) RunbookPlan {
	return newRunbookPlan(vt, view, runbookPlanRenderModePlan)
}

func NewRunbookShow(vt arguments.ViewType, view *View) RunbookPlan {
	return newRunbookPlan(vt, view, runbookPlanRenderModeShow)
}

func newRunbookPlan(vt arguments.ViewType, view *View, mode runbookPlanRenderMode) RunbookPlan {
	switch vt {
	case arguments.ViewJSON:
		if mode == runbookPlanRenderModeShow {
			return &RunbookShowJSON{view: view}
		}
		return &RunbookPlanJSON{view: NewJSONView(view), mode: mode}
	case arguments.ViewHuman:
		return &RunbookPlanHuman{view: view, mode: mode}
	default:
		panic(fmt.Sprintf("unknown view type %v", vt))
	}
}

type runbookPlanRenderMode string

const (
	runbookPlanRenderModePlan runbookPlanRenderMode = "plan"
	runbookPlanRenderModeShow runbookPlanRenderMode = "show"
)

type RunbookPlanHuman struct {
	view *View
	info []runbookPlanInfo
	mode runbookPlanRenderMode
}

func (v *RunbookPlanHuman) UI() runbookgraph.UI                   { return v }
func (v *RunbookPlanHuman) Hooks() []runbookgraph.Hook            { return nil }
func (v *RunbookPlanHuman) Diagnostics(diags tfdiags.Diagnostics) { v.view.Diagnostics(diags) }
func (v *RunbookPlanHuman) HelpPrompt() {
	if v.mode == runbookPlanRenderModeShow {
		v.view.HelpPrompt("runbook show")
		return
	}
	v.view.HelpPrompt("runbook plan")
}
func (v *RunbookPlanHuman) PlannedStep(step *runbookruntime.Step)          {}
func (v *RunbookPlanHuman) ExecutingStep(step *runbookruntime.Step)        {}
func (v *RunbookPlanHuman) ExecutedStep(step *runbookruntime.Step)         {}
func (v *RunbookPlanHuman) ActionEvent(event runbookgraph.ActionExecEvent) {}
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
	if info.Details != cty.NilVal {
		details := pruneUnsetValue(info.Details)
		if encoded, err := ctyjson.Marshal(details, details.Type()); err == nil {
			entry.Details = encoded
			_ = json.Unmarshal(encoded, &entry.details)
		}
	}
	v.info = append(v.info, entry)
}
func (v *RunbookPlanHuman) Plan(plan *runbookgraph.Plan) {
	renderRunbookPlanHuman(v.view, buildRunbookPlan(plan, v.info), v.mode)
}

type RunbookPlanJSON struct {
	view *JSONView
	info []runbookPlanInfo
	mode runbookPlanRenderMode
}

func (v *RunbookPlanJSON) UI() runbookgraph.UI                            { return v }
func (v *RunbookPlanJSON) Hooks() []runbookgraph.Hook                     { return nil }
func (v *RunbookPlanJSON) Diagnostics(diags tfdiags.Diagnostics)          { v.view.Diagnostics(diags) }
func (v *RunbookPlanJSON) HelpPrompt()                                    {}
func (v *RunbookPlanJSON) PlannedStep(step *runbookruntime.Step)          {}
func (v *RunbookPlanJSON) ExecutingStep(step *runbookruntime.Step)        {}
func (v *RunbookPlanJSON) ExecutedStep(step *runbookruntime.Step)         {}
func (v *RunbookPlanJSON) ActionEvent(event runbookgraph.ActionExecEvent) {}
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
	if info.Details != cty.NilVal {
		details := pruneUnsetValue(info.Details)
		if encoded, err := ctyjson.Marshal(details, details.Type()); err == nil {
			entry.Details = encoded
			_ = json.Unmarshal(encoded, &entry.details)
		}
	}
	v.info = append(v.info, entry)
}
func (v *RunbookPlanJSON) Plan(plan *runbookgraph.Plan) {
	msg := "Runbook plan"
	typ := "runbook_plan"
	if v.mode == runbookPlanRenderModeShow {
		msg = "Runbook show"
		typ = "runbook_show"
	}
	v.view.log.Info(msg, "type", typ, "plan", buildRunbookPlan(plan, v.info))
}

func renderRunbookPlanHuman(view *View, viewPlan runbookPlan, mode runbookPlanRenderMode) {
	if mode == runbookPlanRenderModeShow {
		view.streams.Println("Saved runbook plan")
	} else {
		view.streams.Println("Runbook plan")
	}
	view.streams.Println("")
	if len(viewPlan.Steps) == 0 {
		if mode == runbookPlanRenderModeShow {
			view.streams.Println("The saved runbook plan has no planned steps.")
		} else {
			view.streams.Println("No changes. The runbook has no planned steps.")
		}
		return
	}
	view.streams.Println("Steps:")
	view.streams.Println("")

	infoByStep := make(map[string][]runbookPlanInfo)
	for _, info := range viewPlan.Info {
		key := fmt.Sprintf("%s[%d]", info.StepName, info.StepIndex)
		infoByStep[key] = append(infoByStep[key], info)
	}

	reads, lists, executes := 0, 0, 0
	for _, step := range viewPlan.Steps {
		stepInfo := dedupePlanInfo(infoByStep[fmt.Sprintf("%s[%d]", step.Name, step.Index)])
		view.streams.Println(renderRunbookStepPlan(step, stepInfo))
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
	prefix := "Plan"
	if mode == runbookPlanRenderModeShow {
		prefix = "Saved plan"
	}
	view.streams.Println(fmt.Sprintf("%s: %d to run, %d to skip. Operations: %d to read, %d to list, %d to execute.", prefix, countRunnableSteps(viewPlan.Steps), countSkippedSteps(viewPlan.Steps), reads, lists, executes))
}

type RunbookShowJSON struct {
	view *View
	info []runbookPlanInfo
}

func (v *RunbookShowJSON) UI() runbookgraph.UI                            { return nil }
func (v *RunbookShowJSON) Hooks() []runbookgraph.Hook                     { return nil }
func (v *RunbookShowJSON) Diagnostics(diags tfdiags.Diagnostics)          { v.view.Diagnostics(diags) }
func (v *RunbookShowJSON) HelpPrompt()                                    {}
func (v *RunbookShowJSON) PlannedStep(step *runbookruntime.Step)          {}
func (v *RunbookShowJSON) ExecutingStep(step *runbookruntime.Step)        {}
func (v *RunbookShowJSON) ExecutedStep(step *runbookruntime.Step)         {}
func (v *RunbookShowJSON) ActionEvent(event runbookgraph.ActionExecEvent) {}
func (v *RunbookShowJSON) PlannedStepInfo(info runbookgraph.StepPlanInfo) {
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
	if info.Details != cty.NilVal {
		details := pruneUnsetValue(info.Details)
		if encoded, err := ctyjson.Marshal(details, details.Type()); err == nil {
			entry.Details = encoded
			_ = json.Unmarshal(encoded, &entry.details)
		}
	}
	v.info = append(v.info, entry)
}
func (v *RunbookShowJSON) Plan(plan *runbookgraph.Plan) {
	ret := buildRunbookPlan(plan, v.info)
	enc, err := json.MarshalIndent(ret, "", "  ")
	if err != nil {
		v.view.streams.Eprintf("Failed to marshal runbook show json: %s", err)
		return
	}
	v.view.streams.Println(string(enc))
}

func buildRunbookPlan(plan *runbookgraph.Plan, info []runbookPlanInfo) runbookPlan {
	ret := runbookPlan{}
	if len(info) != 0 {
		ret.Info = append([]runbookPlanInfo(nil), info...)
	} else if plan != nil && len(plan.PlanInfo) != 0 {
		ret.Info = make([]runbookPlanInfo, 0, len(plan.PlanInfo))
		for _, raw := range plan.PlanInfo {
			entry := runbookPlanInfo{
				StepName:  raw.StepName,
				StepIndex: raw.StepIndex,
				Type:      raw.Type,
				Subject:   raw.Subject,
				Status:    raw.Status,
				valueVal:  raw.Value,
			}
			if raw.Value != cty.NilVal {
				if encoded, err := json.Marshal(tfdiags.CompactValueStr(raw.Value)); err == nil {
					entry.Value = encoded
				}
			}
			if raw.Details != cty.NilVal {
				details := pruneUnsetValue(raw.Details)
				if encoded, err := ctyjson.Marshal(details, details.Type()); err == nil {
					entry.Details = encoded
					_ = json.Unmarshal(encoded, &entry.details)
				}
			}
			ret.Info = append(ret.Info, entry)
		}
	}
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
		Name:         step.Name,
		Index:        step.Index,
		Status:       step.Status,
		SkipReason:   step.SkipReason,
		outputValues: make(map[string]string),
	}
	if step.InstanceKey != nil {
		ret.InstanceKey = step.InstanceKey.String()
	}
	if step.Outputs != cty.NilVal && step.Outputs.IsKnown() && !step.Outputs.IsNull() && step.Outputs.Type().IsObjectType() {
		ret.Outputs = make(map[string]json.RawMessage)
		for name, value := range step.Outputs.AsValueMap() {
			compact := tfdiags.CompactValueStr(value)
			encoded, err := json.Marshal(compact)
			if err == nil {
				ret.Outputs[name] = encoded
				ret.outputValues[name] = compact
			}
		}
	}
	return ret
}

func renderRunbookStepPlan(step runbookPlanStep, info []runbookPlanInfo) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  - %s (%s)", renderStepAddress(step), renderStepOutcome(step)))
	if step.SkipReason != "" {
		b.WriteString(fmt.Sprintf(": %s", step.SkipReason))
	}
	b.WriteString("\n")

	sections := classifyRunbookStepInfo(info)
	if len(sections.reads) != 0 {
		b.WriteString("      reads:\n")
		for _, item := range sections.reads {
			b.WriteString(fmt.Sprintf("        - %s\n", renderPlanInfo(item, 0)))
		}
	}
	if len(sections.lists) != 0 {
		b.WriteString("      lists:\n")
		for _, item := range sections.lists {
			b.WriteString(fmt.Sprintf("        - %s\n", renderPlanInfo(item, 0)))
		}
	}
	if len(sections.actions) != 0 {
		b.WriteString("      actions:\n")
		for _, item := range sections.actions {
			b.WriteString(fmt.Sprintf("        - %s\n", item.Subject))
			if item.valueVal != cty.NilVal && item.valueVal.IsKnown() && !item.valueVal.IsNull() {
				b.WriteString("          config:\n")
				for _, line := range strings.Split(repl.FormatValue(pruneUnsetValue(item.valueVal), 0), "\n") {
					b.WriteString(fmt.Sprintf("            %s\n", line))
				}
			}
		}
	}
	if len(sections.executions) != 0 {
		b.WriteString("      executions:\n")
		for _, item := range sections.executions {
			b.WriteString(fmt.Sprintf("        - invoke %s\n", item.Subject))
		}
	}
	if len(step.outputValues) != 0 {
		b.WriteString("      outputs:\n")
		for _, name := range sortedOutputNames(step.outputValues) {
			b.WriteString(fmt.Sprintf("        - %s = %s\n", name, step.outputValues[name]))
		}
	}
	return b.String()
}

type runbookStepSections struct {
	reads      []runbookPlanInfo
	lists      []runbookPlanInfo
	actions    []runbookPlanInfo
	executions []runbookPlanInfo
}

func classifyRunbookStepInfo(info []runbookPlanInfo) runbookStepSections {
	var ret runbookStepSections
	actionBySubject := map[string]runbookPlanInfo{}
	for _, item := range sortedPlanInfo(info) {
		switch item.Type {
		case "data", "workspace_read":
			ret.reads = append(ret.reads, item)
		case "list":
			ret.lists = append(ret.lists, item)
		case "action":
			actionBySubject[item.Subject] = item
		case "execute":
			ret.executions = append(ret.executions, item)
			if existing, ok := actionBySubject[item.Subject]; ok {
				if existing.valueVal == cty.NilVal && item.valueVal != cty.NilVal {
					existing.valueVal = item.valueVal
					actionBySubject[item.Subject] = existing
				}
			} else {
				actionBySubject[item.Subject] = item
			}
		}
	}
	if len(actionBySubject) != 0 {
		subjects := make([]string, 0, len(actionBySubject))
		for subject := range actionBySubject {
			subjects = append(subjects, subject)
		}
		sort.Strings(subjects)
		for _, subject := range subjects {
			ret.actions = append(ret.actions, actionBySubject[subject])
		}
	}
	return ret
}

func sortedOutputNames(outputs map[string]string) []string {
	ret := make([]string, 0, len(outputs))
	for name := range outputs {
		ret = append(ret, name)
	}
	sort.Strings(ret)
	return ret
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
	case "workspace_read":
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
	case "workspace_read":
		if info.details != nil {
			kind := "object"
			if rawKind, ok := info.details["kind"].(string); ok && rawKind != "" {
				kind = rawKind
			}
			attrs := ""
			if rawAttrs, ok := info.details["attributes"].([]any); ok && len(rawAttrs) > 0 {
				parts := make([]string, 0, len(rawAttrs))
				for _, raw := range rawAttrs {
					if attr, ok := raw.(string); ok && attr != "" {
						parts = append(parts, attr)
					}
				}
				if len(parts) > 0 {
					attrs = fmt.Sprintf(" attributes=[%s]", strings.Join(parts, ", "))
				}
			}
			return fmt.Sprintf("workspace %s %q%s", kind, info.Subject, attrs)
		}
		return fmt.Sprintf("workspace read %q", info.Subject)
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
	case "data", "workspace_read", "list", "execute":
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
