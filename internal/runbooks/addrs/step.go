package addrs

type Step struct {
	Step StepInstance
}

func (s Step) String() string {
	return "steps." + s.Step.String()
}
