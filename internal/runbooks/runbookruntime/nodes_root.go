// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookruntime

import (
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/runbooks/runbookgraph"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type nodeRunbookRoot struct{}

func (n *nodeRunbookRoot) Hashcode() interface{} { return "runbook.root" }
func (n *nodeRunbookRoot) Name() string          { return "runbook.root" }
func (n *nodeRunbookRoot) Scope() runbookgraph.Scope {
	return runbookgraph.RootScope{}
}
func (n *nodeRunbookRoot) ExecuteGraphNode(*RunbookGraphWalker) tfdiags.Diagnostics {
	return nil
}

type nodeRootVariable struct {
	NameValue string
	Variable  *configs.Variable
}

func (n *nodeRootVariable) Hashcode() interface{} { return "runbook.var." + n.NameValue }
func (n *nodeRootVariable) Name() string          { return "var." + n.NameValue }
func (n *nodeRootVariable) Scope() runbookgraph.Scope {
	return runbookgraph.RootScope{}
}
func (n *nodeRootVariable) ReferenceableAddrs() []runbookgraph.ReferenceTarget {
	return []runbookgraph.ReferenceTarget{addrs.InputVariable{Name: n.NameValue}}
}
func (n *nodeRootVariable) ExecuteGraphNode(*RunbookGraphWalker) tfdiags.Diagnostics {
	return nil
}

type nodeRunbookOutput struct {
	NameValue string
	Output    *configs.Output
}

func (n *nodeRunbookOutput) Hashcode() interface{} { return "runbook.output." + n.NameValue }
func (n *nodeRunbookOutput) Name() string          { return "output." + n.NameValue }
func (n *nodeRunbookOutput) Scope() runbookgraph.Scope {
	return runbookgraph.RootScope{}
}
func (n *nodeRunbookOutput) References() []runbookgraph.Reference {
	if n == nil || n.Output == nil {
		return nil
	}
	return runbookReferencesInExpr(runbookgraph.RootScope{}, n.Output.Expr)
}
func (n *nodeRunbookOutput) ExecuteGraphNode(w *RunbookGraphWalker) tfdiags.Diagnostics {
	if w == nil || w.Data == nil || w.Data.Context == nil {
		return nil
	}
	return w.Data.Context.validateRunbookOutput(n.NameValue)
}
