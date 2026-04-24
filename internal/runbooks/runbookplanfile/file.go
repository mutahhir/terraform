package runbookplanfile

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/hashicorp/terraform/internal/plans"
)

func Read(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, err
	}
	if plan.Version != FormatVersion {
		return nil, fmt.Errorf("unsupported runbook plan format version %d", plan.Version)
	}
	if plan.Sources == nil {
		plan.Sources = map[string][]byte{}
	}
	if plan.WorkspaceSources == nil {
		plan.WorkspaceSources = map[string][]byte{}
	}
	if plan.Variables == nil {
		plan.Variables = map[string]plans.DynamicValue{}
	}
	return &plan, nil
}

func Write(path string, plan *Plan) error {
	if plan == nil {
		return fmt.Errorf("nil runbook plan")
	}
	if plan.Version == 0 {
		plan.Version = FormatVersion
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
