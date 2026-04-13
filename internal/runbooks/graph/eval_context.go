// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/lang"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

// EvalContext tracks the values that are available while evaluating a runbook.
type EvalContext struct {
	config *runbookconfigs.RunbookConfig

	variables     terraform.InputValues
	variablesLock sync.Mutex
}

type EvalContextOpts struct {
	Config *runbookconfigs.RunbookConfig
}

func NewEvalContext(opts EvalContextOpts) *EvalContext {
	return &EvalContext{
		config:        opts.Config,
		variables:     make(terraform.InputValues),
		variablesLock: sync.Mutex{},
	}
}

func (ec *EvalContext) Config() *runbookconfigs.RunbookConfig {
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

func (ec *EvalContext) ProviderInput(addrs.AbsProviderConfig) map[string]cty.Value {
	return nil
}

func (ec *EvalContext) EvaluateBlock(body hcl.Body, schema *configschema.Block) (cty.Value, hcl.Body, tfdiags.Diagnostics) {
	if schema == nil {
		return cty.EmptyObjectVal, body, nil
	}

	scope := &lang.Scope{Data: providerEvalData{ctx: ec}}
	val, diags := scope.EvalBlock(body, schema)
	return val, body, diags
}
