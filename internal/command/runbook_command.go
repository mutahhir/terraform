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

  Commands for planning Terraform runbooks.
`)
}

func (c *RunbookCommand) Synopsis() string {
	return "Runbook operations"
}
