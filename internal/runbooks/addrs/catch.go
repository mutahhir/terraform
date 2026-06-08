package addrs

import "fmt"

// FailedStep is a referenceable address for the failed_step object
// that is available within catch blocks. It represents the step that
// triggered the catch handler.
type FailedStep struct{}

func (FailedStep) String() string {
	return "failed_step"
}

// CatchAddr represents a catch block address (catch.<name>).
type CatchAddr struct {
	Name string
}

func (c CatchAddr) String() string {
	return fmt.Sprintf("catch.%s", c.Name)
}

// CatchOutput represents a reference to a catch block's output (catch.<name>.<output>).
type CatchOutput struct {
	CatchName  string
	OutputName string
}

func (c CatchOutput) String() string {
	return fmt.Sprintf("catch.%s.%s", c.CatchName, c.OutputName)
}
