package command

import (
	"strings"

	"github.com/hashicorp/cli"
)

type RunbookCommand struct {
	Meta
}

func (c *RunbookCommand) Run(args []string) int {
	return cli.RunResultHelp
}

func (c *RunbookCommand) Help() string {
	return strings.TrimSpace(`
Usage: terraform [global options] runbook <subcommand> [options]

  Commands for initializing, planning, and executing Terraform runbooks.
  Runbook commands operate on the current runbook directory; use the global
  -chdir flag to select a runbook subdirectory from elsewhere.

  Subcommands:
    init      Prepare a runbook working directory
    plan      Build a speculative runbook plan
    execute   Execute the planned runbook steps
    show      Show a saved runbook plan
`)
}

func (c *RunbookCommand) Synopsis() string {
	return "Runbook operations"
}
