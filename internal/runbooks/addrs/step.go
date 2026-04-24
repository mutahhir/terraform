package addrs

type Step struct {
	Step StepInstance
}

func (s Step) String() string {
	return "step." + s.Step.String()
}
