package runbookruntime

import (
	runbookconfigs "github.com/hashicorp/terraform/internal/runbooks/configs"
	"github.com/zclconf/go-cty/cty"
)

type Step struct {
	Config *runbookconfigs.Step

	Name  string
	Index int

	Outputs cty.Value
}
