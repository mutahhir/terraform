package arguments

import "github.com/hashicorp/terraform/internal/tfdiags"

type RunbookExecute struct {
	Vars         *Vars
	ViewType     ViewType
	AutoApprove  bool
	InputEnabled bool
	PlanPath     string
}

func ParseRunbookExecute(args []string) (*RunbookExecute, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	runbook := &RunbookExecute{Vars: &Vars{}}

	cmdFlags := defaultFlagSet("runbook execute")
	varsFlags := NewFlagNameValueSlice("-var")
	varFilesFlags := varsFlags.Alias("-var-file")
	runbook.Vars.vars = &varsFlags
	runbook.Vars.varFiles = &varFilesFlags
	cmdFlags.Var(runbook.Vars.vars, "var", "var")
	cmdFlags.Var(runbook.Vars.varFiles, "var-file", "var-file")
	cmdFlags.BoolVar(&runbook.AutoApprove, "auto-approve", false, "auto-approve")
	cmdFlags.BoolVar(&runbook.InputEnabled, "input", true, "input")

	var json bool
	cmdFlags.BoolVar(&json, "json", false, "json")

	if err := cmdFlags.Parse(args); err != nil {
		diags = diags.Append(tfdiags.Sourceless(tfdiags.Error, "Failed to parse command-line flags", err.Error()))
	}

	switch len(cmdFlags.Args()) {
	case 0:
	case 1:
		runbook.PlanPath = cmdFlags.Args()[0]
	default:
		diags = diags.Append(tfdiags.Sourceless(tfdiags.Error, "Too many command line arguments", "Expected at most one saved runbook plan file path."))
	}

	if json {
		runbook.InputEnabled = false
		runbook.ViewType = ViewJSON
	} else {
		runbook.ViewType = ViewHuman
	}

	if runbook.PlanPath != "" && !runbook.Vars.Empty() {
		diags = diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Variable values not allowed with saved runbook plan",
			"A saved runbook plan already includes the variable values used during planning, so -var and -var-file are not valid when supplying a saved plan file.",
		))
	}

	if json && !runbook.AutoApprove && runbook.PlanPath == "" {
		diags = diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Auto-approve required",
			"Terraform cannot ask for interactive approval when -json is set. Use -auto-approve with runbook execute -json, or pass a saved runbook plan file.",
		))
	}

	return runbook, diags
}
