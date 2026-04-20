package arguments

import "github.com/hashicorp/terraform/internal/tfdiags"

type RunbookExecute struct {
	Vars        *Vars
	ViewType    ViewType
	AutoApprove bool
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

	var json bool
	cmdFlags.BoolVar(&json, "json", false, "json")

	if err := cmdFlags.Parse(args); err != nil {
		diags = diags.Append(tfdiags.Sourceless(tfdiags.Error, "Failed to parse command-line flags", err.Error()))
	}

	if len(cmdFlags.Args()) > 0 {
		diags = diags.Append(tfdiags.Sourceless(tfdiags.Error, "Too many command line arguments", "To specify a working directory for runbook execution, use the global -chdir flag."))
	}

	if json {
		runbook.ViewType = ViewJSON
	} else {
		runbook.ViewType = ViewHuman
	}

	return runbook, diags
}
