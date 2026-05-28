package views

// runbookExecSymbols extends plan symbols with execute-time states.
type runbookExecSymbols struct {
	runbookPlanSymbols

	// Execute-time dot states
	DotWaiting string
	DotRunning string
	DotDone    string
	DotFailed  string

	// Spinner frames
	SpinnerFrames []string

	// Colors for execute states
	ColorRunning string
	ColorDone    string
	ColorFailed  string
	ColorWaiting string
}

func unicodeExecSymbols() runbookExecSymbols {
	return runbookExecSymbols{
		runbookPlanSymbols: unicodePlanSymbols(),
		DotWaiting:         "○",
		DotRunning:         "◉",
		DotDone:            "●",
		DotFailed:          "✗",
		SpinnerFrames:      []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		ColorRunning:       "[cyan]",
		ColorDone:          "[green]",
		ColorFailed:        "[red]",
		ColorWaiting:       "[light_gray]",
	}
}

func asciiExecSymbols() runbookExecSymbols {
	return runbookExecSymbols{
		runbookPlanSymbols: asciiPlanSymbols(),
		DotWaiting:         ".",
		DotRunning:         "@",
		DotDone:            "*",
		DotFailed:          "X",
		SpinnerFrames:      []string{"|", "/", "-", "\\"},
		ColorRunning:       "",
		ColorDone:          "",
		ColorFailed:        "",
		ColorWaiting:       "",
	}
}

func selectExecSymbols(view *View) runbookExecSymbols {
	if view.colorize.Disable {
		return asciiExecSymbols()
	}
	return unicodeExecSymbols()
}
