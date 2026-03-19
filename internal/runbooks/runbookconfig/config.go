// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/hashicorp/terraform/internal/tfdiags"
)

type Config struct {
	RootPath  string
	Files     map[string]*File
	Variables map[string]*Variable
	Runbook   *Runbook
}

type File struct {
	Path      string
	Runbook   *Runbook
	Variables map[string]*Variable
	Steps     map[string]*Step
}

type Runbook struct {
	TerraformVersion string
	Providers        []*ProviderConfig
	Variables        map[string]*Variable
	DeclRange        tfdiags.SourceRange
}

type ProviderConfig struct {
	Type      string
	DeclRange tfdiags.SourceRange
}

type Variable struct {
	Name      string
	Default   hcl.Expression
	DeclRange tfdiags.SourceRange
}

type Step struct {
	Name           string
	HasConfig      bool
	ActionCount    int
	ListCount      int
	ExecCount      int
	Actions        []*Action
	Lists          []*List
	ExecuteInvokes []*ExecuteActionInvoke
	Locals         map[string]hcl.Expression
	Outputs        map[string]*Output
	Preconditions  []*Condition
	Postconditions []*Condition
	DeclRange      tfdiags.SourceRange
}

type Action struct {
	Type      string
	Name      string
	Config    hcl.Body
	DeclRange tfdiags.SourceRange
}

func (a *Action) Reference() string {
	if a == nil {
		return ""
	}
	return fmt.Sprintf("action.%s.%s", a.Type, a.Name)
}

type ExecuteActionInvoke struct {
	ActionRef string
	DeclRange tfdiags.SourceRange
}

type List struct {
	Type      string
	Name      string
	Config    hcl.Body
	DeclRange tfdiags.SourceRange
}

type Output struct {
	Name      string
	Value     hcl.Expression
	DeclRange tfdiags.SourceRange
}

type Condition struct {
	Kind         ConditionKind
	Condition    hcl.Expression
	ErrorMessage hcl.Expression
	OnFail       ConditionOnFail
	DeclRange    tfdiags.SourceRange
}

type ConditionKind string

const (
	PreconditionCondition  ConditionKind = "precondition"
	PostconditionCondition ConditionKind = "postcondition"
)

type ConditionOnFail string

const (
	ConditionOnFailError ConditionOnFail = "error"
	ConditionOnFailSkip  ConditionOnFail = "skip"
)

func LoadConfigDir(rootPath string) (*Config, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	entries, err := os.ReadDir(rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, diags.Append(tfdiags.Sourceless(
				tfdiags.Error,
				"Missing runbook configuration",
				fmt.Sprintf("There is no runbook configuration directory at %s.", rootPath),
			))
		}
		return nil, diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Cannot read runbook configuration",
			fmt.Sprintf("Error while reading %s: %s.", rootPath, err),
		))
	}

	ret := &Config{
		RootPath:  rootPath,
		Files:     make(map[string]*File),
		Variables: make(map[string]*Variable),
	}

	for _, entry := range entries {
		if entry.IsDir() || validFilenameSuffix(entry.Name()) == "" {
			continue
		}

		path := filepath.Join(rootPath, entry.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			diags = diags.Append(tfdiags.Sourceless(
				tfdiags.Error,
				"Cannot read runbook configuration",
				fmt.Sprintf("Error while reading %s: %s.", path, err),
			))
			continue
		}

		file, moreDiags := ParseFileSource(src, path)
		diags = diags.Append(moreDiags)
		if file == nil {
			continue
		}

		if ret.Runbook != nil && file.Runbook != nil {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate runbook block",
				Detail:   fmt.Sprintf("A runbook block was already declared at %s.", ret.Runbook.DeclRange.StartString()),
				Subject:  file.Runbook.DeclRange.ToHCL().Ptr(),
			})
		} else if file.Runbook != nil {
			ret.Runbook = file.Runbook
		}

		for name, variable := range file.Variables {
			if existing, exists := ret.Variables[name]; exists {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate variable block",
					Detail:   fmt.Sprintf("A variable named %q was already declared at %s.", name, existing.DeclRange.StartString()),
					Subject:  variable.DeclRange.ToHCL().Ptr(),
				})
				continue
			}
			ret.Variables[name] = variable
		}

		ret.Files[file.Path] = file
	}

	if ret.Runbook == nil {
		diags = diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Missing runbook block",
			"Runbook configuration must declare exactly one runbook block.",
		))
	}

	return ret, diags
}

func ParseFileSource(src []byte, path string) (*File, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	var body hcl.Body
	switch validFilenameSuffix(path) {
	case ".tfrun.hcl":
		hclFile, hclDiags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
		diags = diags.Append(hclDiags)
		if hclDiags.HasErrors() {
			return nil, diags
		}
		body = hclFile.Body
	default:
		return nil, diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Unsupported file type",
			fmt.Sprintf("Cannot load %s as a runbook configuration file.", path),
		))
	}

	return DecodeFileBody(body, path)
}

func DecodeFileBody(body hcl.Body, path string) (*File, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	ret := &File{
		Path:      path,
		Variables: make(map[string]*Variable),
		Steps:     make(map[string]*Step),
	}

	content, hclDiags := body.Content(rootSchema)
	diags = diags.Append(hclDiags)
	if content == nil {
		return ret, diags
	}

	for _, block := range content.Blocks {
		switch block.Type {
		case "runbook":
			if ret.Runbook != nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate runbook block",
					Detail:   fmt.Sprintf("This file already declared a runbook block at %s.", ret.Runbook.DeclRange.StartString()),
					Subject:  block.DefRange.Ptr(),
				})
				continue
			}

			rb, moreDiags := decodeRunbookBlock(block)
			diags = diags.Append(moreDiags)
			ret.Runbook = rb
		case "variable":
			variable, moreDiags := decodeVariableBlock(block)
			diags = diags.Append(moreDiags)
			if variable == nil {
				continue
			}
			if existing, exists := ret.Variables[variable.Name]; exists {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate variable block",
					Detail:   fmt.Sprintf("A variable named %q was already declared at %s.", variable.Name, existing.DeclRange.StartString()),
					Subject:  block.DefRange.Ptr(),
				})
				continue
			}
			ret.Variables[variable.Name] = variable
		case "step":
			step, moreDiags := decodeStepBlock(block)
			diags = diags.Append(moreDiags)
			if step == nil {
				continue
			}
			if existing, exists := ret.Steps[step.Name]; exists {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate step block",
					Detail:   fmt.Sprintf("A step named %q was already declared at %s.", step.Name, existing.DeclRange.StartString()),
					Subject:  block.DefRange.Ptr(),
				})
				continue
			}
			ret.Steps[step.Name] = step
		}
	}

	return ret, diags
}

func decodeRunbookBlock(block *hcl.Block) (*Runbook, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	content, hclDiags := block.Body.Content(runbookSchema)
	diags = diags.Append(hclDiags)

	ret := &Runbook{
		Variables: make(map[string]*Variable),
		DeclRange: tfdiags.SourceRangeFromHCL(block.DefRange),
	}

	if attr, ok := content.Attributes["terraform_version"]; ok {
		var version string
		moreDiags := gohcl.DecodeExpression(attr.Expr, nil, &version)
		diags = diags.Append(moreDiags)
		ret.TerraformVersion = version
	}

	for _, nested := range content.Blocks {
		if nested.Type != "provider" {
			continue
		}
		provider := &ProviderConfig{
			Type:      nested.Labels[0],
			DeclRange: tfdiags.SourceRangeFromHCL(nested.DefRange),
		}
		ret.Providers = append(ret.Providers, provider)
	}

	return ret, diags
}

func decodeVariableBlock(block *hcl.Block) (*Variable, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := &Variable{
		Name:      block.Labels[0],
		DeclRange: tfdiags.SourceRangeFromHCL(block.DefRange),
	}
	if !hclsyntax.ValidIdentifier(ret.Name) {
		return nil, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid variable name",
			Detail:   "Variable names must be valid identifiers.",
			Subject:  &block.LabelRanges[0],
		})
	}

	content, hclDiags := block.Body.Content(variableSchema)
	diags = diags.Append(hclDiags)
	if content == nil {
		return ret, diags
	}
	if attr, ok := content.Attributes["default"]; ok {
		ret.Default = attr.Expr
	}
	return ret, diags
}

func decodeStepBlock(block *hcl.Block) (*Step, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	ret := &Step{
		Name:      block.Labels[0],
		Locals:    make(map[string]hcl.Expression),
		Outputs:   make(map[string]*Output),
		DeclRange: tfdiags.SourceRangeFromHCL(block.DefRange),
	}
	if !hclsyntax.ValidIdentifier(ret.Name) {
		return nil, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid step name",
			Detail:   "Step names must be valid identifiers.",
			Subject:  &block.LabelRanges[0],
		})
	}

	content, hclDiags := block.Body.Content(stepSchema)
	diags = diags.Append(hclDiags)

	for _, nested := range content.Blocks {
		switch nested.Type {
		case "config":
			ret.HasConfig = true
		case "locals":
			attrs, moreDiags := nested.Body.JustAttributes()
			diags = diags.Append(moreDiags)
			for name, attr := range attrs {
				ret.Locals[name] = attr.Expr
			}
		case "action":
			ret.ActionCount++
			action, moreDiags := decodeRunbookActionBlock(nested)
			diags = diags.Append(moreDiags)
			if action != nil {
				ret.Actions = append(ret.Actions, action)
			}
		case "list":
			ret.ListCount++
			list, moreDiags := decodeRunbookListBlock(nested)
			diags = diags.Append(moreDiags)
			if list != nil {
				ret.Lists = append(ret.Lists, list)
			}
		case "execute":
			ret.ExecCount++
			invokes, moreDiags := decodeExecuteBlock(nested)
			diags = diags.Append(moreDiags)
			ret.ExecuteInvokes = append(ret.ExecuteInvokes, invokes...)
		case "output":
			output, moreDiags := decodeOutputBlock(nested)
			diags = diags.Append(moreDiags)
			if output == nil {
				continue
			}
			if existing, exists := ret.Outputs[output.Name]; exists {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate output block",
					Detail:   fmt.Sprintf("An output named %q was already declared at %s.", output.Name, existing.DeclRange.StartString()),
					Subject:  nested.DefRange.Ptr(),
				})
				continue
			}
			ret.Outputs[output.Name] = output
		case "precondition":
			cond, moreDiags := decodeConditionBlock(nested, PreconditionCondition)
			diags = diags.Append(moreDiags)
			if cond != nil {
				ret.Preconditions = append(ret.Preconditions, cond)
			}
		case "postcondition":
			cond, moreDiags := decodeConditionBlock(nested, PostconditionCondition)
			diags = diags.Append(moreDiags)
			if cond != nil {
				ret.Postconditions = append(ret.Postconditions, cond)
			}
		}
	}

	return ret, diags
}

func decodeConditionBlock(block *hcl.Block, kind ConditionKind) (*Condition, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	content, hclDiags := block.Body.Content(conditionSchema)
	diags = diags.Append(hclDiags)
	if content == nil {
		return nil, diags
	}

	cond := &Condition{
		Kind:      kind,
		DeclRange: tfdiags.SourceRangeFromHCL(block.DefRange),
		OnFail:    ConditionOnFailError,
	}

	if attr, exists := content.Attributes["condition"]; exists {
		cond.Condition = attr.Expr
		if len(cond.Condition.Variables()) == 0 {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("Invalid %s expression", kind),
				Detail:   "The condition expression must refer to at least one object from elsewhere in the runbook.",
				Subject:  cond.Condition.Range().Ptr(),
			})
		}
	}

	if attr, exists := content.Attributes["error_message"]; exists {
		cond.ErrorMessage = attr.Expr
	}

	if attr, exists := content.Attributes["on_fail"]; exists {
		var onFail string
		moreDiags := gohcl.DecodeExpression(attr.Expr, nil, &onFail)
		diags = diags.Append(moreDiags)
		switch ConditionOnFail(onFail) {
		case ConditionOnFailError, ConditionOnFailSkip:
			cond.OnFail = ConditionOnFail(onFail)
		default:
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("Invalid %s on_fail value", kind),
				Detail:   "The on_fail argument must be either \"error\" or \"skip\".",
				Subject:  attr.Expr.Range().Ptr(),
			})
		}
	}

	if kind == PostconditionCondition && cond.OnFail == ConditionOnFailSkip {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid postcondition on_fail value",
			Detail:   "Postconditions cannot use on_fail = \"skip\" because they are evaluated after execute.",
			Subject:  block.DefRange.Ptr(),
		})
	}

	return cond, diags
}

func decodeOutputBlock(block *hcl.Block) (*Output, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := &Output{
		Name:      block.Labels[0],
		DeclRange: tfdiags.SourceRangeFromHCL(block.DefRange),
	}
	if !hclsyntax.ValidIdentifier(ret.Name) {
		return nil, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid output name",
			Detail:   "Output names must be valid identifiers.",
			Subject:  &block.LabelRanges[0],
		})
	}
	content, hclDiags := block.Body.Content(outputSchema)
	diags = diags.Append(hclDiags)
	if content == nil {
		return ret, diags
	}
	ret.Value = content.Attributes["value"].Expr
	return ret, diags
}

func decodeRunbookActionBlock(block *hcl.Block) (*Action, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := &Action{
		Type:      block.Labels[0],
		Name:      block.Labels[1],
		Config:    block.Body,
		DeclRange: tfdiags.SourceRangeFromHCL(block.DefRange),
	}
	if !hclsyntax.ValidIdentifier(ret.Type) {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid action type name",
			Detail:   "Action types must be valid identifiers.",
			Subject:  &block.LabelRanges[0],
		})
	}
	if !hclsyntax.ValidIdentifier(ret.Name) {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid action name",
			Detail:   "Action names must be valid identifiers.",
			Subject:  &block.LabelRanges[1],
		})
	}
	return ret, diags
}

func decodeRunbookListBlock(block *hcl.Block) (*List, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := &List{
		Type:      block.Labels[0],
		Name:      block.Labels[1],
		Config:    block.Body,
		DeclRange: tfdiags.SourceRangeFromHCL(block.DefRange),
	}
	if !hclsyntax.ValidIdentifier(ret.Type) {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid list type name",
			Detail:   "List types must be valid identifiers.",
			Subject:  &block.LabelRanges[0],
		})
	}
	if !hclsyntax.ValidIdentifier(ret.Name) {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid list name",
			Detail:   "List names must be valid identifiers.",
			Subject:  &block.LabelRanges[1],
		})
	}
	return ret, diags
}

func decodeExecuteBlock(block *hcl.Block) ([]*ExecuteActionInvoke, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	content, hclDiags := block.Body.Content(executeSchema)
	diags = diags.Append(hclDiags)
	if content == nil {
		return nil, diags
	}

	var ret []*ExecuteActionInvoke
	for _, nested := range content.Blocks {
		if nested.Type != "action_invoke" {
			continue
		}
		invoke, moreDiags := decodeExecuteActionInvokeBlock(nested)
		diags = diags.Append(moreDiags)
		if invoke != nil {
			ret = append(ret, invoke)
		}
	}
	return ret, diags
}

func decodeExecuteActionInvokeBlock(block *hcl.Block) (*ExecuteActionInvoke, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	content, hclDiags := block.Body.Content(executeActionInvokeSchema)
	diags = diags.Append(hclDiags)
	if content == nil {
		return nil, diags
	}
	attr := content.Attributes["action"]
	traversal, travDiags := hcl.AbsTraversalForExpr(attr.Expr)
	diags = diags.Append(travDiags)
	if travDiags.HasErrors() {
		return nil, diags
	}
	if len(traversal) != 3 {
		return nil, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid action reference",
			Detail:   "The action argument must refer to an action in the form action.<type>.<name>.",
			Subject:  attr.Expr.Range().Ptr(),
		})
	}
	root, ok := traversal[0].(hcl.TraverseRoot)
	if !ok || root.Name != "action" {
		return nil, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid action reference",
			Detail:   "The action argument must refer to an action in the form action.<type>.<name>.",
			Subject:  attr.Expr.Range().Ptr(),
		})
	}
	typeStep, ok := traversal[1].(hcl.TraverseAttr)
	if !ok {
		return nil, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid action reference",
			Detail:   "The action argument must refer to an action in the form action.<type>.<name>.",
			Subject:  attr.Expr.Range().Ptr(),
		})
	}
	nameStep, ok := traversal[2].(hcl.TraverseAttr)
	if !ok {
		return nil, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid action reference",
			Detail:   "The action argument must refer to an action in the form action.<type>.<name>.",
			Subject:  attr.Expr.Range().Ptr(),
		})
	}
	return &ExecuteActionInvoke{
		ActionRef: fmt.Sprintf("action.%s.%s", typeStep.Name, nameStep.Name),
		DeclRange: tfdiags.SourceRangeFromHCL(block.DefRange),
	}, diags
}

func validFilenameSuffix(filename string) string {
	const nativeSuffix = ".tfrun.hcl"

	switch {
	case strings.HasSuffix(filename, nativeSuffix):
		return nativeSuffix
	default:
		return ""
	}
}

var rootSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "runbook"},
		{Type: "variable", LabelNames: []string{"name"}},
		{Type: "step", LabelNames: []string{"name"}},
	},
}

var runbookSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "terraform_version"},
	},
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "provider", LabelNames: []string{"type"}},
	},
}

var variableSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{{Name: "default"}},
}

var stepSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "config"},
		{Type: "locals"},
		{Type: "action", LabelNames: []string{"type", "name"}},
		{Type: "list", LabelNames: []string{"type", "name"}},
		{Type: "execute"},
		{Type: "output", LabelNames: []string{"name"}},
		{Type: "precondition"},
		{Type: "postcondition"},
	},
}

var executeSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{{Type: "action_invoke"}},
}

var executeActionInvokeSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{{Name: "action", Required: true}},
}

var conditionSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "condition", Required: true},
		{Name: "error_message", Required: true},
		{Name: "on_fail"},
	},
}

var outputSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{{Name: "value", Required: true}},
}
