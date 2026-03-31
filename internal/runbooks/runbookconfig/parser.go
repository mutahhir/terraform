package runbookconfig

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/spf13/afero"
)

type RunbookParser struct {
	fs afero.Afero
	p  *hclparse.Parser
}

func NewRunbookParser(fs afero.Fs) *RunbookParser {
	if fs == nil {
		fs = afero.OsFs{}
	}

	return &RunbookParser{
		fs: afero.Afero{Fs: fs},
		p:  hclparse.NewParser(),
	}
}

func (p *RunbookParser) LoadHCLFile(path string) (hcl.Body, hcl.Diagnostics) {
	src, err := p.fs.ReadFile(path)

	if err != nil {
		return nil, hcl.Diagnostics{
			{
				Severity: hcl.DiagError,
				Summary:  "Failed to read file",
				Detail:   fmt.Sprintf("The file %q could not be read.", path),
			},
		}
	}

	var file *hcl.File
	var diags hcl.Diagnostics
	file, diags = p.p.ParseHCL(src, path)

	// If the returned file or body is nil, then we'll return a non-nil empty
	// body so we'll meet our contract that nil means an error reading the file.
	if file == nil || file.Body == nil {
		return hcl.EmptyBody(), diags
	}

	return file.Body, diags
}

func (p *RunbookParser) loadRunbookConfigFile(path string) (*RunbookFile, hcl.Diagnostics) {
	body, diags := p.LoadHCLFile(path)
	if body == nil {
		return nil, diags
	}

	return p.parseRunbookConfigFile(body, diags)
}

func (p *RunbookParser) LoadRunbookConfigDir(path, rootModulePath string) (*RunbookConfig, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	infos, err := p.fs.ReadDir(path)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Failed to read runbook directory",
			Detail:   fmt.Sprintf("The runbook directory %q could not be read.", path),
		}}
	}

	var files []*RunbookFile
	for _, info := range infos {
		if info.IsDir() {
			continue
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".tfrun.hcl") || strings.HasPrefix(name, ".") {
			continue
		}

		file, fileDiags := p.loadRunbookConfigFile(filepath.Join(path, name))
		diags = append(diags, fileDiags...)
		if file != nil {
			files = append(files, file)
		}
	}

	result, moreDiags := NewRunbook(files)
	diags = append(diags, moreDiags...)
	if result == nil {
		return nil, diags
	}

	// Load the terraform workspace so we can reference things like workspace.actions
	workspaceParser := configs.NewParser(p.fs.Fs)
	rootModule, workspaceDiags := workspaceParser.LoadConfigDir(rootModulePath)
	diags = append(diags, workspaceDiags...)
	if rootModule != nil {
		workspaceCfg, buildDiags := configs.BuildConfig(rootModule, configs.DisabledModuleWalker, nil)
		diags = append(diags, buildDiags...)
		result.WorkspaceConfig = workspaceCfg
	}

	result.RunbookSourceDir = path
	result.WorkspaceSourceDir = rootModulePath

	return result, diags
}
