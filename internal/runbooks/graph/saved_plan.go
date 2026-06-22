package runbookgraph

import (
	"fmt"
	"sort"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/plans"
	"github.com/hashicorp/terraform/internal/providers"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	runbookplanfile "github.com/hashicorp/terraform/internal/runbooks/runbookplanfile"
	runbookruntime "github.com/hashicorp/terraform/internal/runbooks/runtime"
	"github.com/hashicorp/terraform/internal/states"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

func ExportSavedPlan(plan *Plan, sources map[string][]byte, inputValues terraform.InputValues) (*runbookplanfile.Plan, error) {
	if plan == nil || plan.evalCtx == nil {
		return nil, fmt.Errorf("missing runbook plan context")
	}
	ret := &runbookplanfile.Plan{
		Version:          runbookplanfile.FormatVersion,
		RunbookSourceDir: plan.Config.RunbookSourceDir,
		Sources:          copySourcesMap(sources),
		Variables:        make(map[string]plans.DynamicValue),
		Steps:            make([]*runbookplanfile.Step, 0, len(plan.Steps)),
		PlanInfo:         make([]*runbookplanfile.StepPlanInfo, 0, len(plan.PlanInfo)),
	}
	for name, input := range inputValues {
		if input == nil {
			continue
		}
		raw, err := plans.NewDynamicValue(input.Value, cty.DynamicPseudoType)
		if err != nil {
			return nil, fmt.Errorf("encode variable %s: %w", name, err)
		}
		ret.Variables[name] = raw
	}
	for _, info := range plan.PlanInfo {
		rec, err := exportPlanInfo(info)
		if err != nil {
			return nil, err
		}
		ret.PlanInfo = append(ret.PlanInfo, rec)
	}

	plan.evalCtx.stepsLock.RLock()
	defer plan.evalCtx.stepsLock.RUnlock()
	for _, key := range plan.evalCtx.stepOrder {
		state := plan.evalCtx.steps[key]
		if state == nil || state.runtime == nil {
			continue
		}
		step, err := exportStepState(state)
		if err != nil {
			return nil, err
		}
		ret.Steps = append(ret.Steps, step)
	}
	return ret, nil
}

func ImportSavedPlan(config *runbookconfigs.RunbookConfig, saved *runbookplanfile.Plan, workspaceState *states.State, providersMap map[terraformaddrs.Provider]providers.Factory) (*Plan, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if config == nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Missing runbook config", "A saved runbook plan requires configuration."))
	}
	if saved == nil {
		return nil, diags.Append(tfdiags.Sourceless(tfdiags.Error, "Missing runbook saved plan", "A saved runbook plan file is required."))
	}
	inputValues := make(terraform.InputValues, len(saved.Variables))
	for name, raw := range saved.Variables {
		val, err := raw.Decode(cty.DynamicPseudoType)
		if err != nil {
			return nil, diags.Append(err)
		}
		inputValues[name] = &terraform.InputValue{Value: val}
	}
	evalCtx, ctxDiags := newPlanEvalContext(EvalContextOpts{Config: config, WorkspaceState: workspaceState}, inputValues, providersMap)
	diags = diags.Append(ctxDiags)
	if diags.HasErrors() {
		return nil, diags
	}
	for _, savedStep := range saved.Steps {
		if savedStep == nil {
			continue
		}
		if err := importStepState(evalCtx, config, savedStep); err != nil {
			return nil, diags.Append(err)
		}
	}
	for _, rawInfo := range saved.PlanInfo {
		if rawInfo == nil {
			continue
		}
		info, err := importPlanInfo(rawInfo)
		if err != nil {
			return nil, diags.Append(err)
		}
		evalCtx.planInfo = append(evalCtx.planInfo, info)
	}
	graph, graphDiags := buildSavedGraph(config, saved.Steps)
	diags = diags.Append(graphDiags)
	if diags.HasErrors() {
		return nil, diags
	}
	for _, vertex := range graph.Vertices() {
		rewireExactStepOutputReferences(graph, vertex)
	}
	return &Plan{Config: config, Graph: graph, Steps: evalCtx.StepsInOrder(), PlanInfo: evalCtx.PlanInfo(), evalCtx: evalCtx}, diags
}

func exportPlanInfo(info StepPlanInfo) (*runbookplanfile.StepPlanInfo, error) {
	ret := &runbookplanfile.StepPlanInfo{
		StepName:  info.StepName,
		StepIndex: info.StepIndex,
		Type:      info.Type,
		Subject:   info.Subject,
		Status:    info.Status,
	}
	if info.Value != cty.NilVal {
		raw, err := plans.NewDynamicValue(info.Value, cty.DynamicPseudoType)
		if err != nil {
			return nil, fmt.Errorf("encode step plan info value: %w", err)
		}
		ret.Value = raw
	}
	if info.Details != cty.NilVal {
		raw, err := plans.NewDynamicValue(info.Details, cty.DynamicPseudoType)
		if err != nil {
			return nil, fmt.Errorf("encode step plan info details: %w", err)
		}
		ret.Details = raw
	}
	return ret, nil
}

func importPlanInfo(raw *runbookplanfile.StepPlanInfo) (StepPlanInfo, error) {
	ret := StepPlanInfo{
		StepName:  raw.StepName,
		StepIndex: raw.StepIndex,
		Type:      raw.Type,
		Subject:   raw.Subject,
		Status:    raw.Status,
	}
	if raw.Value != nil {
		val, err := raw.Value.Decode(cty.DynamicPseudoType)
		if err != nil {
			return ret, err
		}
		ret.Value = val
	}
	if raw.Details != nil {
		val, err := raw.Details.Decode(cty.DynamicPseudoType)
		if err != nil {
			return ret, err
		}
		ret.Details = val
	}
	return ret, nil
}

func exportStepState(state *stepEvalState) (*runbookplanfile.Step, error) {
	ret := &runbookplanfile.Step{
		Name:           state.runtime.Name,
		Index:          state.runtime.Index,
		Status:         state.runtime.Status,
		SkipReason:     state.runtime.SkipReason,
		InstanceKey:    exportInstanceKey(state.runtime.InstanceKey),
		RepetitionData: exportRepetitionData(state.runtime.RepetitionData),
		Data:           make(map[string]plans.DynamicValue),
		Lists:          make(map[string]plans.DynamicValue),
		Outputs:        make(map[string]plans.DynamicValue),
		Actions:        make(map[string]*runbookplanfile.ActionState),
	}
	for name, value := range state.data {
		raw, err := plans.NewDynamicValue(value, cty.DynamicPseudoType)
		if err != nil {
			return nil, fmt.Errorf("encode step data %s: %w", name, err)
		}
		ret.Data[name] = raw
	}
	for name, value := range state.lists {
		raw, err := plans.NewDynamicValue(value, cty.DynamicPseudoType)
		if err != nil {
			return nil, fmt.Errorf("encode step list %s: %w", name, err)
		}
		ret.Lists[name] = raw
	}
	for name, value := range state.outputs {
		raw, err := plans.NewDynamicValue(value, cty.DynamicPseudoType)
		if err != nil {
			return nil, fmt.Errorf("encode step output %s: %w", name, err)
		}
		ret.Outputs[name] = raw
	}
	for name, action := range state.actions {
		if action == nil {
			continue
		}
		rec := &runbookplanfile.ActionState{Planned: action.planned, Invoked: action.invoked}
		if action.plannedConfig != cty.NilVal {
			raw, err := plans.NewDynamicValue(action.plannedConfig, cty.DynamicPseudoType)
			if err != nil {
				return nil, fmt.Errorf("encode planned action config %s: %w", name, err)
			}
			rec.PlannedConfig = raw
		}
		ret.Actions[name] = rec
	}
	return ret, nil
}

func importStepState(evalCtx *BuiltinEvalContext, config *runbookconfigs.RunbookConfig, saved *runbookplanfile.Step) error {
	instanceKey, err := importInstanceKey(saved.InstanceKey)
	if err != nil {
		return err
	}
	runtimeStep := &runbookruntime.Step{
		Config:         config.Steps[saved.Name],
		Name:           saved.Name,
		Index:          saved.Index,
		InstanceKey:    instanceKey,
		RepetitionData: importRepetitionData(saved.RepetitionData),
		Status:         saved.Status,
		SkipReason:     saved.SkipReason,
		Outputs:        cty.NilVal,
	}
	key := stepStateKey(saved.Name, instanceKey)
	evalCtx.stepsLock.Lock()
	state := evalCtx.ensureStepStateLocked(key)
	state.runtime = runtimeStep
	if len(state.outputs) == 0 {
		state.outputs = make(map[string]cty.Value)
	}
	if len(state.data) == 0 {
		state.data = make(map[string]cty.Value)
	}
	if len(state.lists) == 0 {
		state.lists = make(map[string]cty.Value)
	}
	if len(state.actions) == 0 {
		state.actions = make(map[string]*actionEvalState)
	}
	evalCtx.stepsLock.Unlock()
	for name, raw := range saved.Outputs {
		val, err := raw.Decode(cty.DynamicPseudoType)
		if err != nil {
			return err
		}
		evalCtx.setStepOutputWithKey(saved.Name, instanceKey, name, val)
	}
	evalCtx.stepsLock.Lock()
	defer evalCtx.stepsLock.Unlock()
	state = evalCtx.ensureStepStateLocked(key)
	for name, raw := range saved.Data {
		val, err := raw.Decode(cty.DynamicPseudoType)
		if err != nil {
			return err
		}
		state.data[name] = val
	}
	for name, raw := range saved.Lists {
		val, err := raw.Decode(cty.DynamicPseudoType)
		if err != nil {
			return err
		}
		state.lists[name] = val
	}
	for name, raw := range saved.Actions {
		if raw == nil {
			continue
		}
		action := &actionEvalState{planned: raw.Planned, invoked: raw.Invoked}
		if raw.PlannedConfig != nil {
			val, err := raw.PlannedConfig.Decode(cty.DynamicPseudoType)
			if err != nil {
				return err
			}
			action.plannedConfig = val
		}
		state.actions[name] = action
	}
	return nil
}

func exportInstanceKey(key terraformaddrs.InstanceKey) *runbookplanfile.InstanceKey {
	if key == nil || key == terraformaddrs.NoKey {
		return nil
	}
	switch v := key.(type) {
	case terraformaddrs.StringKey:
		return &runbookplanfile.InstanceKey{Kind: "string", Str: string(v)}
	case terraformaddrs.IntKey:
		return &runbookplanfile.InstanceKey{Kind: "int", Int: int(v)}
	default:
		return &runbookplanfile.InstanceKey{Kind: key.String(), Str: key.String()}
	}
}

func importInstanceKey(key *runbookplanfile.InstanceKey) (terraformaddrs.InstanceKey, error) {
	if key == nil || key.Kind == "" {
		return terraformaddrs.NoKey, nil
	}
	switch key.Kind {
	case "string":
		return terraformaddrs.StringKey(key.Str), nil
	case "int":
		return terraformaddrs.IntKey(key.Int), nil
	default:
		return nil, fmt.Errorf("unsupported instance key kind %q", key.Kind)
	}
}

func exportRepetitionData(data *terraform.InstanceKeyEvalData) *runbookplanfile.RepetitionData {
	if data == nil {
		return nil
	}
	ret := &runbookplanfile.RepetitionData{}
	if data.CountIndex != cty.NilVal && data.CountIndex.IsKnown() && !data.CountIndex.IsNull() {
		count, _ := data.CountIndex.AsBigFloat().Int64()
		ret.CountIndex = &count
	}
	if data.EachKey != cty.NilVal && data.EachKey.IsKnown() && !data.EachKey.IsNull() {
		ret.EachKey = data.EachKey.AsString()
	}
	if data.EachValue != cty.NilVal {
		raw, err := plans.NewDynamicValue(data.EachValue, cty.DynamicPseudoType)
		if err == nil {
			ret.EachValue = raw
		}
	}
	if ret.CountIndex == nil && ret.EachKey == "" && ret.EachValue == nil {
		return nil
	}
	return ret
}

func importRepetitionData(data *runbookplanfile.RepetitionData) *terraform.InstanceKeyEvalData {
	if data == nil {
		return nil
	}
	ret := &terraform.InstanceKeyEvalData{}
	if data.CountIndex != nil {
		ret.CountIndex = cty.NumberIntVal(*data.CountIndex)
	}
	if data.EachKey != "" {
		ret.EachKey = cty.StringVal(data.EachKey)
	}
	if data.EachValue != nil {
		val, err := data.EachValue.Decode(cty.DynamicPseudoType)
		if err == nil {
			ret.EachValue = val
		}
	}
	return ret
}

func buildSavedGraph(config *runbookconfigs.RunbookConfig, steps []*runbookplanfile.Step) (*terraform.Graph, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	builder := &savedPlanGraphBuilder{Config: config, Steps: steps}
	graph, buildDiags := builder.Build()
	diags = diags.Append(buildDiags)
	return graph, diags
}

type savedPlanGraphBuilder struct {
	Config *runbookconfigs.RunbookConfig
	Steps  []*runbookplanfile.Step
}

func (b *savedPlanGraphBuilder) Build() (*terraform.Graph, tfdiags.Diagnostics) {
	return (&terraform.BasicGraphBuilder{
		Steps: []terraform.GraphTransformer{
			&terraform.RootVariableTransformer{Config: rootVariableConfig(b.Config)},
			&SavedPlanStepTransformer{Config: b.Config, Steps: b.Steps},
			&PlanOutputTransformer{Config: b.Config},
			&terraform.RootTransformer{},
			&terraform.TransitiveReductionTransformer{},
		},
		Name: "RunbookSavedPlanBuilder",
	}).Build(terraformaddrs.RootModuleInstance)
}

type SavedPlanStepTransformer struct {
	Config *runbookconfigs.RunbookConfig
	Steps  []*runbookplanfile.Step
}

func (t *SavedPlanStepTransformer) Transform(g *terraform.Graph) error {
	if t == nil || t.Config == nil {
		return nil
	}
	ordered := append([]*runbookplanfile.Step(nil), t.Steps...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i] == nil || ordered[j] == nil {
			return i < j
		}
		if ordered[i].Index == ordered[j].Index {
			return ordered[i].Name < ordered[j].Name
		}
		return ordered[i].Index < ordered[j].Index
	})
	for _, saved := range ordered {
		if saved == nil {
			continue
		}
		cfg := t.Config.Steps[saved.Name]
		instanceKey, err := importInstanceKey(saved.InstanceKey)
		if err != nil {
			return err
		}
		runtimeStep := &runbookruntime.Step{
			Config:         cfg,
			Name:           saved.Name,
			Index:          saved.Index,
			InstanceKey:    instanceKey,
			RepetitionData: importRepetitionData(saved.RepetitionData),
			Status:         saved.Status,
			SkipReason:     saved.SkipReason,
		}
		if len(saved.Outputs) != 0 {
			outs := make(map[string]cty.Value, len(saved.Outputs))
			for name, raw := range saved.Outputs {
				val, err := raw.Decode(cty.DynamicPseudoType)
				if err != nil {
					return err
				}
				outs[name] = val
			}
			runtimeStep.Outputs = cty.ObjectVal(outs)
		}
		subgraph, err := buildExpandedStepInstanceGraph(saved.Name, cfg, expandedStepInstance{key: instanceKey, repetitionData: runtimeStep.RepetitionData, runtime: runtimeStep})
		if err != nil {
			return err
		}
		subsumeExpandedGraph(g, subgraph)
	}
	return nil
}

func copySourcesMap(in map[string][]byte) map[string][]byte {
	if len(in) == 0 {
		return map[string][]byte{}
	}
	ret := make(map[string][]byte, len(in))
	for k, v := range in {
		copyV := make([]byte, len(v))
		copy(copyV, v)
		ret[k] = copyV
	}
	return ret
}
