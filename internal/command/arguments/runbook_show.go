package arguments

import "github.com/hashicorp/terraform/internal/tfdiags"

type RunbookShow struct {
	ViewType ViewType
	PlanPath string
}

func ParseRunbookShow(args []string) (*RunbookShow, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	show := &RunbookShow{}

	cmdFlags := defaultFlagSet("runbook show")
	var json bool
	cmdFlags.BoolVar(&json, "json", false, "json")

	if err := cmdFlags.Parse(args); err != nil {
		diags = diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Failed to parse command-line flags",
			err.Error(),
		))
	}

	switch len(cmdFlags.Args()) {
	case 0:
		diags = diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Missing saved runbook plan file",
			"The runbook show command requires a saved runbook plan file path.",
		))
	case 1:
		show.PlanPath = cmdFlags.Args()[0]
	default:
		diags = diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Too many command line arguments",
			"Expected exactly one saved runbook plan file path.",
		))
	}

	if json {
		show.ViewType = ViewJSON
	} else {
		show.ViewType = ViewHuman
	}

	return show, diags
}
