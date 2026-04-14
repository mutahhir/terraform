package addrs

import "fmt"

type StepOutput struct {
	Step       StepInstance
	OutputName string
}

func (s StepOutput) String() string {
	return fmt.Sprintf("step.%s.%s", s.Step.String(), s.OutputName)
}
