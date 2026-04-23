package arguments

import "github.com/hashicorp/terraform/internal/tfdiags"

type RunbookInit struct {
	ViewType ViewType
	Upgrade  bool
}

func ParseRunbookInit(args []string) (*RunbookInit, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	runbook := &RunbookInit{}

	cmdFlags := defaultFlagSet("runbook init")
	cmdFlags.BoolVar(&runbook.Upgrade, "upgrade", false, "upgrade")

	var json bool
	cmdFlags.BoolVar(&json, "json", false, "json")

	if err := cmdFlags.Parse(args); err != nil {
		diags = diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Failed to parse command-line flags",
			err.Error(),
		))
	}

	if len(cmdFlags.Args()) > 0 {
		diags = diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Too many command line arguments",
			"To specify a working directory for runbook initialization, use the global -chdir flag.",
		))
	}

	if json {
		runbook.ViewType = ViewJSON
	} else {
		runbook.ViewType = ViewHuman
	}

	return runbook, diags
}
