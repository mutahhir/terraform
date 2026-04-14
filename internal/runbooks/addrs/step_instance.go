package addrs

import (
	"fmt"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
)

type StepInstance struct {
	StepName    string
	InstanceKey terraformaddrs.InstanceKey
}

func (s StepInstance) String() string {
	if s.InstanceKey == nil {
		return s.StepName
	}
	return fmt.Sprintf("%s%s", s.StepName, s.InstanceKey.String())
}
