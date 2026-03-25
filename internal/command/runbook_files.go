// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package command

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/runbooks"
	"github.com/hashicorp/terraform/internal/runbooks/runbookplanfile"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/mitchellh/colorstring"
	"github.com/zclconf/go-cty/cty"
)

const (
	runbookStateFilename = "runbook-state.json"
	runbookPlanFilename  = "runbook.tfrunplan"
)

type localRunbookState struct {
	Version      int                 `json:"version"`
	Status       string              `json:"status"`
	ConfigPath   string              `json:"config_path"`
	Workspace    string              `json:"workspace"`
	CreatedAt    string              `json:"created_at"`
	UpdatedAt    string              `json:"updated_at"`
	StepOrder    []string            `json:"step_order"`
	Dependencies map[string][]string `json:"dependencies"`
}

func (c *RunbookCommand) runbookStatePath() string {
	return filepath.Join(c.DataDir(), runbookStateFilename)
}

func (c *RunbookCommand) runbookPlanPath() string {
	return filepath.Join(c.DataDir(), runbookPlanFilename)
}

func (c *RunbookCommand) loadRunbookState() (*localRunbookState, error) {
	src, err := os.ReadFile(c.runbookStatePath())
	if err != nil {
		return nil, err
	}
	var state localRunbookState
	if err := json.Unmarshal(src, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (c *RunbookCommand) saveRunbookState(state *localRunbookState) error {
	if err := os.MkdirAll(c.DataDir(), 0o755); err != nil {
		return err
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	buf, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	return os.WriteFile(c.runbookStatePath(), buf, 0o600)
}

func (c *RunbookCommand) removeRunbookState() error {
	if err := os.Remove(c.runbookStatePath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
		b.WriteString(color.Color(fmt.Sprintf("[bold][cyan]# Step %d: %s[reset]\n", i+1, step.Name)))
		if len(step.After) > 0 {
			b.WriteString(fmt.Sprintf("    after           = [%s]\n", strings.Join(step.After, ", ")))
		}
		if step.KnownSkipped {
			skippedCount++
			b.WriteString(color.Color("    status          = [yellow]\"skipped\"[reset]\n"))
			if step.SkipReason != "" {
				b.WriteString(fmt.Sprintf("    reason          = %q\n", step.SkipReason))
			}
		} else {
			executableCount++
			b.WriteString(color.Color("    status          = [green]\"planned\"[reset]\n"))
		}
		if len(step.PlannedActions) > 0 {
			b.WriteString("    actions = [\n")
			for _, action := range step.PlannedActions {
				b.WriteString(fmt.Sprintf("      %q,\n", action))
			}
			b.WriteString("    ]\n")
		}
		if len(step.PlannedQueries) > 0 {
			b.WriteString("    queries = [\n")
			for _, query := range step.PlannedQueries {
				b.WriteString(fmt.Sprintf("      %q,\n", query))
			}
			b.WriteString("    ]\n")
		}
		if len(step.PlannedData) > 0 {
			b.WriteString("    data_reads = [\n")
			for _, data := range step.PlannedData {
				b.WriteString(fmt.Sprintf("      %q,\n", data))
			}
			b.WriteString("    ]\n")
		}
		if len(step.Outputs) > 0 {
			b.WriteString("    outputs = [\n")
			for _, output := range step.Outputs {
				b.WriteString(fmt.Sprintf("      %q,\n", output))
			}
			b.WriteString("    ]\n")
		}
		if i < len(manifest.Steps)-1 {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(color.Color(fmt.Sprintf("[bold][green]Plan:[reset] %d to execute, %d to skip.\n", executableCount, skippedCount)))
	return b.String()
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
		keys = append(keys, name)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(color.Color(fmt.Sprintf("[bold]Outputs for %s:[reset]\n", stepName)))
	for _, name := range keys {
		b.WriteString(fmt.Sprintf("  %s = %s\n", name, tfdiags.CompactValueStr(vals[name])))
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatActionInvocations(color *colorstring.Colorize, actions []string) string {
	if len(actions) == 0 {
		return ""
	}
	if color == nil {
		color = &colorstring.Colorize{Disable: true}
	}
	vals := append([]string(nil), actions...)
	sort.Strings(vals)

	var b strings.Builder
	b.WriteString(color.Color("[bold]Action invocations:[reset]\n"))
	for _, action := range vals {
		b.WriteString(fmt.Sprintf("  - %s\n", action))
	}
	return strings.TrimRight(b.String(), "\n")
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
