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

  Subcommands:
    init      Prepare a runbook working directory
    plan      Build a speculative runbook plan
    execute   Execute the planned runbook steps
`)
}

func (c *RunbookCommand) Synopsis() string {
	return "Runbook operations"
}
