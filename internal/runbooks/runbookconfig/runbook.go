package runbookconfig

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
)

// RunbookFile describes the contents of a single Runbook HCL file.
// Borrowing the concept from internal/configs
type RunbookFile struct {
	CoreVersionConstraints []configs.VersionConstraint
	ProviderConfigs        []*configs.Provider
	RequiredProviders      []*configs.RequiredProviders

	Variables []*configs.Variable
	Outputs   []*configs.Output
	Steps     []*Step
}

type RunbookConfig struct {
	WorkspaceSourceDir string
	WorkspaceConfig    *configs.Config
	// We can have multiple runbook directories within a single root module
	// The boundary for runbooks is all tfrun.hcl files within the same
	// directory constitute the same runbook
	RunbookSourceDir string

	Steps map[string]*Step

	// Reusing a lot of Terraform configurations
	Outputs              map[string]*configs.Output
	Variables            map[string]*configs.Variable
	ProviderConfigs      map[string]*configs.Provider
	ProviderRequirements *configs.RequiredProviders
	ProviderLocalNames   map[addrs.Provider]string
}

func NewRunbook(files []*RunbookFile) (*RunbookConfig, hcl.Diagnostics) {
	ret := &RunbookConfig{
		Steps:              make(map[string]*Step),
		Outputs:            make(map[string]*configs.Output),
		Variables:          make(map[string]*configs.Variable),
		ProviderConfigs:    make(map[string]*configs.Provider),
		ProviderLocalNames: make(map[addrs.Provider]string),
	}

	var diags hcl.Diagnostics

	for _, file := range files {
		for _, step := range file.Steps {
			if existing, exists := ret.Steps[step.Name]; exists {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate step declaration",
					Detail:   "A step named " + step.Name + " was already declared at " + existing.DeclRange.String() + ".",
					Subject:  step.DeclRange.Ptr(),
				})
				continue
			}
			ret.Steps[step.Name] = step
		}

		for _, output := range file.Outputs {
			if existing, exists := ret.Outputs[output.Name]; exists {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate output declaration",
					Detail:   "An output named " + output.Name + " was already declared at " + existing.DeclRange.String() + ".",
					Subject:  output.DeclRange.Ptr(),
				})
				continue
			}
			ret.Outputs[output.Name] = output
		}

		for _, variable := range file.Variables {
			if existing, exists := ret.Variables[variable.Name]; exists {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate variable declaration",
					Detail:   "A variable named " + variable.Name + " was already declared at " + existing.DeclRange.String() + ".",
					Subject:  variable.DeclRange.Ptr(),
				})
				continue
			}
			ret.Variables[variable.Name] = variable
		}

		for _, provider := range file.ProviderConfigs {
			key := provider.Name
			if provider.Alias != "" {
				key += "." + provider.Alias
			}
			if existing, exists := ret.ProviderConfigs[key]; exists {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate provider configuration",
					Detail:   "A provider configuration for " + key + " was already declared at " + existing.DeclRange.String() + ".",
					Subject:  provider.DeclRange.Ptr(),
				})
				continue
			}
			ret.ProviderConfigs[key] = provider
		}

		for _, reqs := range file.RequiredProviders {
			if ret.ProviderRequirements != nil {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate required providers configuration",
					Detail:   "A runbook may have only one required_providers block.",
					Subject:  reqs.DeclRange.Ptr(),
				})
				continue
			}
			ret.ProviderRequirements = reqs
		}
	}

	if ret.ProviderRequirements == nil {
		ret.ProviderRequirements = &configs.RequiredProviders{
			RequiredProviders: make(map[string]*configs.RequiredProvider),
		}
	}

	for name, provider := range ret.ProviderRequirements.RequiredProviders {
		ret.ProviderLocalNames[provider.Type] = name
	}

	return ret, diags
}
