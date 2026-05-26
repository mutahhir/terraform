package runbookconfigs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	version "github.com/hashicorp/go-version"
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
	switch {
	case strings.HasSuffix(path, ".json"):
		file, diags = p.p.ParseJSON(src, path)
	default:
		file, diags = p.p.ParseHCL(src, path)
	}

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

func (p *RunbookParser) LoadRunbookConfigDir(path string) (*RunbookConfig, hcl.Diagnostics) {
	files, diags := p.loadRunbookConfigFiles(path)

	result, moreDiags := NewRunbook(files)
	diags = append(diags, moreDiags...)
	if result == nil {
		return nil, diags
	}

	result.RunbookSourceDir = path
	return result, diags
}

func (p *RunbookParser) loadRunbookConfigFiles(path string) ([]*RunbookFile, hcl.Diagnostics) {
	var files []*RunbookFile
	var diags hcl.Diagnostics

	err := afero.Walk(p.fs.Fs, path, func(currentPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if currentPath == path {
			return nil
		}

		name := info.Name()
		if strings.HasPrefix(name, ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(name, ".tfrun.hcl") {
			return nil
		}

		file, fileDiags := p.loadRunbookConfigFile(currentPath)
		diags = append(diags, fileDiags...)
		if file != nil {
			files = append(files, file)
		}
		return nil
	})
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Failed to read runbook directory",
			Detail:   fmt.Sprintf("The runbook directory %q could not be read.", path),
		}}
	}

	return files, diags
}

func (p *RunbookParser) LoadRunbookConfigSources(rootDir string, sources map[string][]byte) (*RunbookConfig, hcl.Diagnostics) {
	fs, diags := loadSourceMapFS("runbook", sources)
	if diags.HasErrors() {
		return nil, diags
	}
	clone := NewRunbookParser(fs)
	return clone.LoadRunbookConfigDir(rootDir)
}

func (p *RunbookParser) LoadWorkspaceReferencesConfigSources(rootModulePath string, sources map[string][]byte) (*configs.Config, hcl.Diagnostics) {
	fs, diags := loadSourceMapFS("workspace", sources)
	if diags.HasErrors() {
		return nil, diags
	}
	clone := NewRunbookParser(fs)
	return clone.LoadWorkspaceReferencesConfig(rootModulePath)
}

func loadSourceMapFS(kind string, sources map[string][]byte) (afero.Fs, hcl.Diagnostics) {
	fs := afero.NewMemMapFs()
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: fmt.Sprintf("Failed to reconstruct %s sources", kind), Detail: err.Error()}}
		}
		if err := afero.WriteFile(fs, path, sources[path], 0o644); err != nil {
			return nil, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: fmt.Sprintf("Failed to reconstruct %s sources", kind), Detail: err.Error()}}
		}
	}
	return fs, nil
}

func (p *RunbookParser) LoadWorkspaceReferencesConfig(rootModulePath string) (*configs.Config, hcl.Diagnostics) {
	workspaceParser := configs.NewParser(p.fs.Fs)
	rootModule, diags := workspaceParser.LoadConfigDir(rootModulePath)
	if rootModule == nil {
		return nil, diags
	}

	workspaceCfg, buildDiags := configs.BuildConfig(rootModule, configs.ModuleWalkerFunc(
		func(req *configs.ModuleRequest) (*configs.Module, *version.Version, hcl.Diagnostics) {
			// For now, runbooks support only already-present local module sources.
			// Local module paths are resolved relative to the calling module.
			basePath := rootModulePath
			if req.Parent != nil && req.Parent.Module != nil && req.Parent.Module.SourceDir != "" {
				basePath = req.Parent.Module.SourceDir
			}
			sourcePath := filepath.Join(basePath, req.SourceAddr.String())
			mod, loadDiags := workspaceParser.LoadConfigDir(sourcePath)
			return mod, nil, loadDiags
		},
	), nil)
	diags = append(diags, buildDiags...)
	return workspaceCfg, diags
}
