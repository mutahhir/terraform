package addrs

import "fmt"

type StepOutput struct {
	StepName   string
	OutputName string
}

func (s StepOutput) String() string {
	return fmt.Sprintf("step.%s.%s", s.StepName, s.OutputName)
}
