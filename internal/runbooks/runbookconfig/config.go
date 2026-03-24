// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/spf13/afero"
	"github.com/zclconf/go-cty/cty"

	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

type Config struct {
	RootPath  string
	Actions   map[string]*Action
	Files     map[string]*File
	Variables map[string]*Variable
	Runbook   *Runbook
}

type File struct {
	Path      string
	Runbook   *Runbook
	Providers map[string]*ProviderConfig
	Variables map[string]*Variable
	Actions   map[string]*Action
	Steps     map[string]*Step
}

type Runbook struct {
	TerraformVersion  string
	RequiredProviders []*RequiredProvider
	Variables         map[string]*Variable
	DeclRange         tfdiags.SourceRange
}

type ProviderConfig struct {
	Type      string
	Alias     string
	Src       []byte
	DeclRange tfdiags.SourceRange
}

type RequiredProvider struct {
	Name      string
	Source    string
	DeclRange tfdiags.SourceRange
}

type Variable struct {
	Name      string
	Default   hcl.Expression
	Src       []byte
	DeclRange tfdiags.SourceRange
}

type Step struct {
	Name           string
	ForEach        hcl.Expression
	ForEachSrc     []byte
	HasConfig      bool
	ActionCount    int
	DataCount      int
	ListCount      int
	ExecCount      int
	Actions        []*Action
	DataSources    []*DataSource
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
	Src       []byte
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
	Src       []byte
	DeclRange tfdiags.SourceRange
}

type DataSource struct {
	Type      string
	Name      string
	Config    hcl.Body
	Src       []byte
	DeclRange tfdiags.SourceRange
}

func (d *DataSource) Reference() string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("data.%s.%s", d.Type, d.Name)
}

type Output struct {
	Name      string
	Value     hcl.Expression
	ValueSrc  []byte
	DeclRange tfdiags.SourceRange
}

type Condition struct {
	Kind            ConditionKind
	Condition       hcl.Expression
	ConditionSrc    []byte
	ErrorMessage    hcl.Expression
	ErrorMessageSrc []byte
	OnFail          ConditionOnFail
	DeclRange       tfdiags.SourceRange
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
		Actions:   make(map[string]*Action),
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

		for ref, action := range file.Actions {
			if existing, exists := ret.Actions[ref]; exists {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate action block",
					Detail:   fmt.Sprintf("An action %q was already declared at %s.", ref, existing.DeclRange.StartString()),
					Subject:  action.DeclRange.ToHCL().Ptr(),
				})
				continue
			}
			ret.Actions[ref] = action
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

func LoadConfigSources(rootPath string, sources map[string][]byte) (*Config, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	ret := &Config{
		RootPath:  rootPath,
		Actions:   make(map[string]*Action),
		Files:     make(map[string]*File),
		Variables: make(map[string]*Variable),
	}

	paths := make([]string, 0, len(sources))
	for name := range sources {
		paths = append(paths, name)
	}
	sort.Strings(paths)

	for _, name := range paths {
		src := sources[name]
		path := filepath.Join(rootPath, filepath.FromSlash(name))

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

		for ref, action := range file.Actions {
			if existing, exists := ret.Actions[ref]; exists {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate action block",
					Detail:   fmt.Sprintf("An action %q was already declared at %s.", ref, existing.DeclRange.StartString()),
					Subject:  action.DeclRange.ToHCL().Ptr(),
				})
				continue
			}
			ret.Actions[ref] = action
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

	return DecodeFileBody(src, body, path)
}

func DecodeFileBody(src []byte, body hcl.Body, path string) (*File, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	var syntaxBlocks map[string]map[string]*hclsyntax.Block
	if syntaxBody, ok := body.(*hclsyntax.Body); ok {
		syntaxBlocks = indexSyntaxBlocks(syntaxBody.Blocks)
	}

	ret := &File{
		Path:      path,
		Providers: make(map[string]*ProviderConfig),
		Variables: make(map[string]*Variable),
		Actions:   make(map[string]*Action),
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
			variable, moreDiags := decodeVariableBlock(src, block, syntaxBlocks)
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
		case "provider":
			provider, moreDiags := decodeProviderBlock(src, block, syntaxBlocks)
			diags = diags.Append(moreDiags)
			if provider == nil {
				continue
			}
			key := providerConfigKey(provider.Type, provider.Alias)
			if existing, exists := ret.Providers[key]; exists {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate provider block",
					Detail:   fmt.Sprintf("A provider configuration for %q was already declared at %s.", key, existing.DeclRange.StartString()),
					Subject:  block.DefRange.Ptr(),
				})
				continue
			}
			ret.Providers[key] = provider
		case "step":
			step, moreDiags := decodeStepBlock(src, block, syntaxBlocks)
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
		case "action":
			action, moreDiags := decodeActionBlock(src, block, syntaxBlocks)
			diags = diags.Append(moreDiags)
			if action == nil {
				continue
			}
			if existing, exists := ret.Actions[action.Reference()]; exists {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate action block",
					Detail:   fmt.Sprintf("An action %q was already declared at %s.", action.Reference(), existing.DeclRange.StartString()),
					Subject:  block.DefRange.Ptr(),
				})
				continue
			}
			ret.Actions[action.Reference()] = action
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
		if nested.Type == "required_providers" {
			requiredProviders, moreDiags := decodeRequiredProvidersBlock(nested)
			diags = diags.Append(moreDiags)
			ret.RequiredProviders = append(ret.RequiredProviders, requiredProviders...)
		}
	}

	return ret, diags
}

func decodeRequiredProvidersBlock(block *hcl.Block) ([]*RequiredProvider, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	attrs, hclDiags := block.Body.JustAttributes()
	diags = diags.Append(hclDiags)
	if hclDiags.HasErrors() {
		return nil, diags
	}

	ret := make([]*RequiredProvider, 0, len(attrs))
	for name, attr := range attrs {
		entries, moreDiags := hcl.ExprMap(attr.Expr)
		diags = diags.Append(moreDiags)
		if moreDiags.HasErrors() {
			continue
		}
		req := &RequiredProvider{Name: name, DeclRange: tfdiags.SourceRangeFromHCL(attr.Range)}
		for _, kv := range entries {
			key, keyDiags := kv.Key.Value(nil)
			diags = diags.Append(keyDiags)
			if keyDiags.HasErrors() || key.Type() != cty.String {
				continue
			}
			if key.AsString() != "source" {
				continue
			}
			var source string
			moreDiags := gohcl.DecodeExpression(kv.Value, nil, &source)
			diags = diags.Append(moreDiags)
			req.Source = source
		}
		ret = append(ret, req)
	}
	return ret, diags
}

func decodeProviderBlock(src []byte, block *hcl.Block, syntaxBlocks map[string]map[string]*hclsyntax.Block) (*ProviderConfig, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	syntaxBlock := lookupSyntaxBlock(syntaxBlocks, block)
	ret := &ProviderConfig{
		Type:      block.Labels[0],
		DeclRange: tfdiags.SourceRangeFromHCL(block.DefRange),
	}
	if len(block.Labels) > 1 {
		ret.Alias = block.Labels[1]
	}
	if src != nil {
		ret.Src = sourceSlice(src, syntaxBlockRange(block, syntaxBlock))
	}
	return ret, diags
}

func decodeActionBlock(src []byte, block *hcl.Block, syntaxBlocks map[string]map[string]*hclsyntax.Block) (*Action, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	syntaxBlock := lookupSyntaxBlock(syntaxBlocks, block)
	ret := &Action{
		Type:      block.Labels[0],
		Name:      block.Labels[1],
		DeclRange: tfdiags.SourceRangeFromHCL(block.DefRange),
	}
	if src != nil {
		ret.Src = sourceSlice(src, syntaxBlockRange(block, syntaxBlock))
	}
	return ret, diags
}

func providerConfigKey(name, alias string) string {
	if alias == "" {
		return name
	}
	return name + "." + alias
}

func WorkspaceActions(cfg *Config) (map[string]*Action, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := make(map[string]*Action)
	if cfg == nil || cfg.RootPath == "" {
		return ret, diags
	}
	workspaceActionSrc := make(map[string][]byte)
	entries, err := os.ReadDir(cfg.RootPath)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !(strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tfquery.hcl")) {
				continue
			}
			path := filepath.Join(cfg.RootPath, name)
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				continue
			}
			hclFile, parseDiags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
			diags = diags.Append(parseDiags)
			if parseDiags.HasErrors() {
				continue
			}
			syntaxBody, ok := hclFile.Body.(*hclsyntax.Body)
			if !ok {
				continue
			}
			syntaxBlocks := indexSyntaxBlocks(syntaxBody.Blocks)
			for _, syntaxBlock := range syntaxBody.Blocks {
				block := &hcl.Block{
					Type:        syntaxBlock.Type,
					Labels:      syntaxBlock.Labels,
					Body:        syntaxBlock.Body,
					DefRange:    syntaxBlock.DefRange(),
					TypeRange:   syntaxBlock.TypeRange,
					LabelRanges: syntaxBlock.LabelRanges,
				}
				if block.Type != "action" {
					continue
				}
				syntaxBlock := lookupSyntaxBlock(syntaxBlocks, block)
				ref := fmt.Sprintf("workspace.action.%s.%s", block.Labels[0], block.Labels[1])
				workspaceActionSrc[ref] = sourceSlice(src, syntaxBlockRange(block, syntaxBlock))
			}
		}
	}
	parser := configs.NewParser(afero.NewOsFs())
	mod, hclDiags := parser.LoadConfigDir(cfg.RootPath)
	diags = diags.Append(hclDiags)
	if mod == nil || hclDiags.HasErrors() {
		return ret, diags
	}
	for _, action := range mod.Actions {
		if action == nil {
			continue
		}
		ref := fmt.Sprintf("workspace.action.%s.%s", action.Type, action.Name)
		ret[ref] = &Action{
			Type:      action.Type,
			Name:      action.Name,
			Config:    action.Config,
			DeclRange: tfdiags.SourceRangeFromHCL(action.DeclRange),
		}
		if src := workspaceActionSrc[ref]; len(src) > 0 {
			ret[ref].Src = src
		}
	}
	return ret, diags
}

func decodeVariableBlock(src []byte, block *hcl.Block, syntaxBlocks map[string]map[string]*hclsyntax.Block) (*Variable, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	syntaxBlock := lookupSyntaxBlock(syntaxBlocks, block)
	ret := &Variable{
		Name:      block.Labels[0],
		Src:       sourceSlice(src, syntaxBlockRange(block, syntaxBlock)),
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

func decodeStepBlock(src []byte, block *hcl.Block, syntaxBlocks map[string]map[string]*hclsyntax.Block) (*Step, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	stepSyntaxBlock := lookupSyntaxBlock(syntaxBlocks, block)
	var nestedSyntaxBlocks map[string]map[string]*hclsyntax.Block
	if stepSyntaxBlock != nil {
		nestedSyntaxBlocks = indexSyntaxBlocks(stepSyntaxBlock.Body.Blocks)
	}

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
	if attr, exists := content.Attributes["for_each"]; exists {
		ret.ForEach = attr.Expr
		ret.ForEachSrc = sourceSlice(src, attr.Expr.Range())
	}

	for _, nested := range content.Blocks {
		syntaxBlock := lookupSyntaxBlock(nestedSyntaxBlocks, nested)
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
			action, moreDiags := decodeRunbookActionBlock(src, nested, syntaxBlock)
			diags = diags.Append(moreDiags)
			if action != nil {
				ret.Actions = append(ret.Actions, action)
			}
		case "list":
			ret.ListCount++
			list, moreDiags := decodeRunbookListBlock(src, nested, syntaxBlock)
			diags = diags.Append(moreDiags)
			if list != nil {
				ret.Lists = append(ret.Lists, list)
			}
		case "data":
			ret.DataCount++
			dataSource, moreDiags := decodeRunbookDataBlock(src, nested, syntaxBlock)
			diags = diags.Append(moreDiags)
			if dataSource != nil {
				ret.DataSources = append(ret.DataSources, dataSource)
			}
		case "execute":
			ret.ExecCount++
			invokes, moreDiags := decodeExecuteBlock(nested)
			diags = diags.Append(moreDiags)
			ret.ExecuteInvokes = append(ret.ExecuteInvokes, invokes...)
		case "output":
			output, moreDiags := decodeOutputBlock(src, nested)
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
			cond, moreDiags := decodeConditionBlock(src, nested, PreconditionCondition)
			diags = diags.Append(moreDiags)
			if cond != nil {
				ret.Preconditions = append(ret.Preconditions, cond)
			}
		case "postcondition":
			cond, moreDiags := decodeConditionBlock(src, nested, PostconditionCondition)
			diags = diags.Append(moreDiags)
			if cond != nil {
				ret.Postconditions = append(ret.Postconditions, cond)
			}
		}
	}

	return ret, diags
}

func decodeConditionBlock(src []byte, block *hcl.Block, kind ConditionKind) (*Condition, tfdiags.Diagnostics) {
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
		cond.ConditionSrc = sourceSlice(src, attr.Expr.Range())
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
		cond.ErrorMessageSrc = sourceSlice(src, attr.Expr.Range())
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

func decodeOutputBlock(src []byte, block *hcl.Block) (*Output, tfdiags.Diagnostics) {
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
	ret.ValueSrc = sourceSlice(src, content.Attributes["value"].Expr.Range())
	return ret, diags
}

func decodeRunbookActionBlock(src []byte, block *hcl.Block, syntaxBlock *hclsyntax.Block) (*Action, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := &Action{
		Type:      block.Labels[0],
		Name:      block.Labels[1],
		Config:    block.Body,
		Src:       sourceSlice(src, syntaxBlockRange(block, syntaxBlock)),
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

func decodeRunbookListBlock(src []byte, block *hcl.Block, syntaxBlock *hclsyntax.Block) (*List, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := &List{
		Type:      block.Labels[0],
		Name:      block.Labels[1],
		Config:    block.Body,
		Src:       sourceSlice(src, syntaxBlockRange(block, syntaxBlock)),
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

func decodeRunbookDataBlock(src []byte, block *hcl.Block, syntaxBlock *hclsyntax.Block) (*DataSource, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := &DataSource{
		Type:      block.Labels[0],
		Name:      block.Labels[1],
		Config:    block.Body,
		DeclRange: tfdiags.SourceRangeFromHCL(block.DefRange),
	}
	if src != nil {
		ret.Src = sourceSlice(src, syntaxBlockRange(block, syntaxBlock))
	}
	if !hclsyntax.ValidIdentifier(ret.Name) {
		return nil, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid data name",
			Detail:   "Data names must be valid identifiers.",
			Subject:  &block.LabelRanges[1],
		})
	}
	return ret, diags
}

func sourceSlice(src []byte, rng hcl.Range) []byte {
	if src == nil || rng.Start.Byte < 0 || rng.End.Byte < rng.Start.Byte || rng.End.Byte > len(src) {
		return nil
	}
	ret := make([]byte, rng.End.Byte-rng.Start.Byte)
	copy(ret, src[rng.Start.Byte:rng.End.Byte])
	return ret
}

func indexSyntaxBlocks(blocks hclsyntax.Blocks) map[string]map[string]*hclsyntax.Block {
	ret := make(map[string]map[string]*hclsyntax.Block)
	for _, block := range blocks {
		key := syntaxBlockKey(block.Type, block.Labels)
		if _, ok := ret[block.Type]; !ok {
			ret[block.Type] = make(map[string]*hclsyntax.Block)
		}
		ret[block.Type][key] = block
	}
	return ret
}

func lookupSyntaxBlock(index map[string]map[string]*hclsyntax.Block, block *hcl.Block) *hclsyntax.Block {
	if index == nil || block == nil {
		return nil
	}
	byType := index[block.Type]
	if byType == nil {
		return nil
	}
	return byType[syntaxBlockKey(block.Type, block.Labels)]
}

func syntaxBlockKey(typ string, labels []string) string {
	return typ + ":" + strings.Join(labels, ":")
}

func syntaxBlockRange(block *hcl.Block, syntaxBlock *hclsyntax.Block) hcl.Range {
	if syntaxBlock != nil {
		return syntaxBlock.Range()
	}
	return block.DefRange
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
	if len(traversal) != 3 && len(traversal) != 4 {
		return nil, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid action reference",
			Detail:   "The action argument must refer to an action in the form action.<type>.<name> or workspace.action.<type>.<name>.",
			Subject:  attr.Expr.Range().Ptr(),
		})
	}
	root, ok := traversal[0].(hcl.TraverseRoot)
	if !ok || (root.Name != "action" && root.Name != "workspace") {
		return nil, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid action reference",
			Detail:   "The action argument must refer to an action in the form action.<type>.<name> or workspace.action.<type>.<name>.",
			Subject:  attr.Expr.Range().Ptr(),
		})
	}
	baseIdx := 1
	prefix := "action"
	if root.Name == "workspace" {
		workspaceStep, ok := traversal[1].(hcl.TraverseAttr)
		if !ok || workspaceStep.Name != "action" {
			return nil, diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid action reference",
				Detail:   "The action argument must refer to an action in the form action.<type>.<name> or workspace.action.<type>.<name>.",
				Subject:  attr.Expr.Range().Ptr(),
			})
		}
		baseIdx = 2
		prefix = "workspace.action"
	}
	typeStep, ok := traversal[baseIdx].(hcl.TraverseAttr)
	if !ok {
		return nil, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid action reference",
			Detail:   "The action argument must refer to an action in the form action.<type>.<name> or workspace.action.<type>.<name>.",
			Subject:  attr.Expr.Range().Ptr(),
		})
	}
	nameStep, ok := traversal[baseIdx+1].(hcl.TraverseAttr)
	if !ok {
		return nil, diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid action reference",
			Detail:   "The action argument must refer to an action in the form action.<type>.<name> or workspace.action.<type>.<name>.",
			Subject:  attr.Expr.Range().Ptr(),
		})
	}
	return &ExecuteActionInvoke{
		ActionRef: fmt.Sprintf("%s.%s.%s", prefix, typeStep.Name, nameStep.Name),
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
		{Type: "provider", LabelNames: []string{"type"}},
		{Type: "variable", LabelNames: []string{"name"}},
		{Type: "action", LabelNames: []string{"type", "name"}},
		{Type: "step", LabelNames: []string{"name"}},
	},
}

var runbookSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "terraform_version"},
	},
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "required_providers"},
	},
}

var variableSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "type"},
		{Name: "description"},
		{Name: "default"},
		{Name: "sensitive"},
		{Name: "nullable"},
	},
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "validation"},
	},
}

var stepSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{{Name: "for_each"}},
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "config"},
		{Type: "locals"},
		{Type: "action", LabelNames: []string{"type", "name"}},
		{Type: "data", LabelNames: []string{"type", "name"}},
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
