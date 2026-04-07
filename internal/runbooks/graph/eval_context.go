// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"sync"

	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookconfig"
	"github.com/hashicorp/terraform/internal/terraform"
)

// EvalContext tracks the values that are available while evaluating a runbook.
type EvalContext struct {
	config *runbookconfig.RunbookConfig

	variables     terraform.InputValues
	variablesLock sync.Mutex
}

type EvalContextOpts struct {
	Config *runbookconfig.RunbookConfig
}

func NewEvalContext(opts EvalContextOpts) *EvalContext {
	return &EvalContext{
		config:        opts.Config,
		variables:     make(terraform.InputValues),
		variablesLock: sync.Mutex{},
	}
}

func (ec *EvalContext) Config() *runbookconfig.RunbookConfig {
	return ec.config
}

func (ec *EvalContext) WorkspaceConfig() *configs.Config {
	if ec.config == nil {
		return nil
	}
	return ec.config.WorkspaceConfig
}

func (ec *EvalContext) SetVariable(name string, value *terraform.InputValue) {
	ec.variablesLock.Lock()
	defer ec.variablesLock.Unlock()

	ec.variables[name] = value
}

func (ec *EvalContext) GetVariable(name string) (*terraform.InputValue, bool) {
	ec.variablesLock.Lock()
	defer ec.variablesLock.Unlock()

	value, ok := ec.variables[name]
	return value, ok
}
