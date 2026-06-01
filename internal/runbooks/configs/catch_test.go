package runbookconfigs

import (
	"testing"

	"github.com/spf13/afero"
)

func TestCatchBlockParsesSuccessfully(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "deploy" {
  action "http" "notify" {}

  execute {
    invoke_action {
      action = action.http.notify
    }
  }
}

catch "alert_oncall" {
  action "pagerduty" "alert" {}

  execute {
    invoke_action {
      action = action.pagerduty.alert
    }
  }
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Error())
	}

	if len(got.Catches) != 1 {
		t.Fatalf("expected 1 catch block, got %d", len(got.Catches))
	}

	catch, ok := got.Catches["alert_oncall"]
	if !ok {
		t.Fatal("expected catch 'alert_oncall' to exist")
	}

	if catch.Name != "alert_oncall" {
		t.Errorf("expected catch name 'alert_oncall', got %q", catch.Name)
	}

	if len(catch.Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(catch.Actions))
	}

	if catch.Actions[0].Type != "pagerduty" || catch.Actions[0].Name != "alert" {
		t.Errorf("expected action pagerduty.alert, got %s.%s", catch.Actions[0].Type, catch.Actions[0].Name)
	}

	if len(catch.Executions) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(catch.Executions))
	}
}

func TestCatchBlockWithPreconditions(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "deploy" {
  action "http" "notify" {}

  execute {
    invoke_action {
      action = action.http.notify
    }
  }
}

catch "rollback" {
  precondition {
    condition     = failed_step.name == "deploy"
    error_message = "Only deploy failures trigger rollback"
  }

  precondition {
    condition     = failed_step.error_summary != ""
    error_message = "Must have an error summary"
  }

  action "terraform" "destroy" {}

  execute {
    invoke_action {
      action = action.terraform.destroy
    }
  }
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Error())
	}

	catch := got.Catches["rollback"]
	if catch == nil {
		t.Fatal("expected catch 'rollback' to exist")
	}

	if len(catch.Preconditions) != 2 {
		t.Fatalf("expected 2 preconditions, got %d", len(catch.Preconditions))
	}

	if catch.Preconditions[0].Kind != PreconditionCondition {
		t.Errorf("expected precondition kind, got %q", catch.Preconditions[0].Kind)
	}
}

func TestCatchBlockWithDataAndOutputs(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "deploy" {
  action "http" "notify" {}

  execute {
    invoke_action {
      action = action.http.notify
    }
  }
}

catch "diagnostics" {
  data "aws_cloudwatch_log_events" "recent" {
    log_group_name = "/app/service"
  }

  locals {
    error_info = failed_step.error_message
  }

  output "bundle" {
    value = "diagnostic data"
  }

  execute {
    read {
      datasource = data.aws_cloudwatch_log_events.recent
    }
  }
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Error())
	}

	catch := got.Catches["diagnostics"]
	if catch == nil {
		t.Fatal("expected catch 'diagnostics' to exist")
	}

	if len(catch.DataSources) != 1 {
		t.Fatalf("expected 1 data source, got %d", len(catch.DataSources))
	}

	if len(catch.Locals) != 1 {
		t.Fatalf("expected 1 local, got %d", len(catch.Locals))
	}

	if len(catch.Outputs) != 1 {
		t.Fatalf("expected 1 output, got %d", len(catch.Outputs))
	}

	if catch.Outputs[0].Name != "bundle" {
		t.Errorf("expected output name 'bundle', got %q", catch.Outputs[0].Name)
	}
}

func TestCatchBlockDuplicateNameProducesDiagnostic(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "deploy" {
  action "http" "notify" {}

  execute {
    invoke_action {
      action = action.http.notify
    }
  }
}

catch "notify" {
  action "slack" "msg" {}

  execute {
    invoke_action {
      action = action.slack.msg
    }
  }
}

catch "notify" {
  action "pagerduty" "alert" {}

  execute {
    invoke_action {
      action = action.pagerduty.alert
    }
  }
}
`)

	p := NewRunbookParser(fs)
	_, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for duplicate catch name, got none")
	}

	found := false
	for _, d := range diags {
		if d.Summary == "Duplicate catch declaration" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'Duplicate catch declaration' diagnostic, got: %s", diags.Error())
	}
}

func TestMultipleCatchBlocksParseSuccessfully(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "deploy" {
  action "http" "notify" {}

  execute {
    invoke_action {
      action = action.http.notify
    }
  }
}

catch "alert" {
  action "pagerduty" "page" {}

  execute {
    invoke_action {
      action = action.pagerduty.page
    }
  }
}

catch "rollback" {
  precondition {
    condition     = failed_step.name == "deploy"
    error_message = "Only for deploy"
  }

  action "terraform" "destroy" {}

  execute {
    invoke_action {
      action = action.terraform.destroy
    }
  }
}

catch "cleanup" {
  action "aws" "delete" {}

  execute {
    invoke_action {
      action = action.aws.delete
    }
  }
}
`)

	p := NewRunbookParser(fs)
	got, diags := p.LoadRunbookConfigDir("/runbook")
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags.Error())
	}

	if len(got.Catches) != 3 {
		t.Fatalf("expected 3 catch blocks, got %d", len(got.Catches))
	}

	for _, name := range []string{"alert", "rollback", "cleanup"} {
		if _, ok := got.Catches[name]; !ok {
			t.Errorf("expected catch %q to exist", name)
		}
	}
}

func TestCatchBlockDuplicateActionProducesDiagnostic(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTestFile(t, fs, "/runbook/main.tfrun.hcl", `
runbook {
  terraform_version = ">= 1.0.0"
}

step "deploy" {
  action "http" "notify" {}

  execute {
    invoke_action {
      action = action.http.notify
    }
  }
}

catch "alert" {
  action "pagerduty" "page" {}
  action "pagerduty" "page" {}

  execute {
    invoke_action {
      action = action.pagerduty.page
    }
  }
}
`)

	p := NewRunbookParser(fs)
	_, diags := p.LoadRunbookConfigDir("/runbook")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for duplicate action, got none")
	}

	found := false
	for _, d := range diags {
		if d.Summary == "Duplicate \"action\" block names" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected duplicate action diagnostic, got: %s", diags.Error())
	}
}
