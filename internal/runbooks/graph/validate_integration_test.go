// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookgraph

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/providers"
	testing_provider "github.com/hashicorp/terraform/internal/providers/testing"
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/spf13/afero"
	"github.com/zclconf/go-cty/cty"
)

func TestValidateIntegrationRunbookProviderConfigValid(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {
  region = "mars"
}

step "deploy" {}
`)

	config := loadIntegrationRunbookConfig(t, fs)
	provider := mockRunbookProviderWithConfigSchema(&configschema.Block{
		Attributes: map[string]*configschema.Attribute{
			"region": {
				Type:     cty.String,
				Required: true,
			},
		},
	})

	diags := Validate(config, &ValidateOpts{
		Providers: map[addrs.Provider]providers.Factory{
			addrs.NewDefaultProvider("test"): fixedProviderFactory(provider),
		},
	})
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Err())
	}
	if !provider.ValidateProviderConfigCalled {
		t.Fatal("expected provider validation to run")
	}
}

func TestValidateIntegrationRunbookProviderConfigInvalid(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeIntegrationTestFile(t, fs, "/workspace/main.tf", ``)
	writeIntegrationTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {
  region = {}
}

step "deploy" {}
`)

	config := loadIntegrationRunbookConfig(t, fs)
	provider := mockRunbookProviderWithConfigSchema(&configschema.Block{
		Attributes: map[string]*configschema.Attribute{
			"region": {
				Type:     cty.String,
				Required: true,
			},
		},
	})

	diags := Validate(config, &ValidateOpts{
		Providers: map[addrs.Provider]providers.Factory{
			addrs.NewDefaultProvider("test"): fixedProviderFactory(provider),
		},
	})
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics but got none")
	}
	if got := diags.Err().Error(); !strings.Contains(got, "Invalid provider configuration") && !strings.Contains(got, "Unsuitable value") && !strings.Contains(got, "Incorrect attribute value type") {
		t.Fatalf("expected provider validation diagnostics, got: %s", got)
	}
}

func loadIntegrationRunbookConfig(t *testing.T, fs afero.Fs) *runbookconfigs.RunbookConfig {
	t.Helper()

	parser := runbookconfigs.NewRunbookParser(fs)
	config, diags := parser.LoadRunbookConfigDir("/runbook")
	if diags.HasErrors() {
		t.Fatalf("unexpected parse diagnostics: %s", diags.Error())
	}
	if config == nil {
		t.Fatal("expected config but got nil")
	}

	workspaceConfig, workspaceDiags := parser.LoadWorkspaceReferencesConfig("/workspace")
	if workspaceDiags.HasErrors() {
		t.Fatalf("unexpected workspace diagnostics: %s", workspaceDiags.Error())
	}
	config.WorkspaceConfig = workspaceConfig
	config.WorkspaceSourceDir = "/workspace"

	return config
}

func mockRunbookProviderWithConfigSchema(schema *configschema.Block) *testing_provider.MockProvider {
	return &testing_provider.MockProvider{
		GetProviderSchemaResponse: &providers.GetProviderSchemaResponse{
			Provider: providers.Schema{Body: schema},
		},
	}
}

func fixedProviderFactory(provider providers.Interface) providers.Factory {
	if p, ok := provider.(*testing_provider.MockProvider); ok {
		p.GetProviderSchemaCalled = false
		p.ValidateProviderConfigCalled = false
		p.ConfigureProviderCalled = false
	}

	return func() (providers.Interface, error) {
		return provider, nil
	}
}
