package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/depsfile"
	"github.com/hashicorp/terraform/internal/getproviders"
)

func TestRunbookInitCommandWritesRunbookArtifacts(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source  = "hashicorp/test"
      version = "1.0.0"
    }
  }
}

provider "test" {}
`)
	t.Chdir(td)

	providerSource, close := newMockProviderSource(t, map[string][]string{
		"test": {"1.0.0"},
	})
	defer close()

	view, done := testView(t)
	c := NewRunbookInitCommand(Meta{View: view, ProviderSource: providerSource})

	code := c.Run([]string{"-no-color"})
	output := done(t)
	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, output.All())
	}

	if _, err := os.Stat(filepath.Join(td, runbookDataDirName)); err != nil {
		t.Fatalf("expected runbook data dir to exist: %s", err)
	}
	if _, err := os.Stat(filepath.Join(td, runbookDependencyLockFilename)); err != nil {
		t.Fatalf("expected runbook lock file to exist: %s", err)
	}

	locks, diags := depsfile.LoadLocksFromFile(filepath.Join(td, runbookDependencyLockFilename))
	if diags.HasErrors() {
		t.Fatalf("unexpected runbook lock diagnostics: %s", diags.Err())
	}
	lock := locks.Provider(addrs.NewDefaultProvider("test"))
	if lock == nil {
		t.Fatal("expected runbook lock file to contain hashicorp/test")
	}
	if got, want := lock.Version().String(), "1.0.0"; got != want {
		t.Fatalf("wrong locked version %q, want %q", got, want)
	}

	installed := filepath.Join(td, runbookDataDirName, "providers", filepath.FromSlash("registry.terraform.io/hashicorp/test/1.0.0/"+getproviders.CurrentPlatform.String()))
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("expected installed provider package at %s: %s", installed, err)
	}
}

func TestRunbookInitCommandRequiresProviderVersionConstraints(t *testing.T) {
	td := t.TempDir()
	writeFile(t, td+"/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}
`)
	t.Chdir(td)

	view, done := testView(t)
	c := NewRunbookInitCommand(Meta{View: view})

	code := c.Run([]string{"-no-color"})
	output := done(t)
	if code != 1 {
		t.Fatalf("expected failure exit code, got %d: %s", code, output.All())
	}
	if !strings.Contains(output.Stderr(), "Missing provider version constraint") {
		t.Fatalf("expected missing version diagnostic, got: %s", output.All())
	}
}
