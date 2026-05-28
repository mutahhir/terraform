package views

// runbookPlanSymbols defines the character set used for rendering the
// runbook plan output. When color is disabled, ASCII fallback is used
// because colorless environments (pipes, CI, etc.) may not render Unicode
// reliably.
type runbookPlanSymbols struct {
	// Execution graph
	Dot       string // Sequential execution point
	DotSkip   string // Skipped execution point
	Timeline  string // Timeline continuation
	Parallel  string // Parallel peer prefix (timeline + space)
	ParSkip   string // Skipped parallel peer prefix

	// Step details
	Action    string // Action invocation
	Read      string // Read operation (data/list)
	Condition string // Condition (pre/post)
	Output    string // Output value

	// Colors (colorstring format strings, empty when no-color)
	ColorStep      string // For step names in execution graph
	ColorSkip      string // For skipped steps
	ColorAction    string // For action invocation symbol + keyword
	ColorRead      string // For read operation symbol + keyword
	ColorCondition string // For condition symbol + keyword
	ColorOutput    string // For output symbol + keyword
	ColorDim       string // For attribute values, secondary info
	ColorHeader    string // For section headers
	ColorReset     string // Reset
}

func unicodePlanSymbols() runbookPlanSymbols {
	return runbookPlanSymbols{
		Dot:            "●",
		DotSkip:        "○",
		Timeline:       "│",
		Parallel:       "│",
		ParSkip:        "│ ○",
		Action:         "⚡",
		Read:           "⇐",
		Condition:      "◇",
		Output:         "↳",
		ColorStep:      "[bold][green]",
		ColorSkip:      "[light_gray]",
		ColorAction:    "[yellow]",
		ColorRead:      "[cyan]",
		ColorCondition: "[magenta]",
		ColorOutput:    "[green]",
		ColorDim:       "[light_gray]",
		ColorHeader:    "[bold][light_blue]",
		ColorReset:     "[reset]",
	}
}

func asciiPlanSymbols() runbookPlanSymbols {
	return runbookPlanSymbols{
		Dot:       "*",
		DotSkip:   "x",
		Timeline:  "|",
		Parallel:  "|",
		ParSkip:   "| x",
		Action:    "!",
		Read:      "<=",
		Condition: "<>",
		Output:    "->",
		// No colors in ASCII mode
		ColorStep:      "",
		ColorSkip:      "",
		ColorAction:    "",
		ColorRead:      "",
		ColorCondition: "",
		ColorOutput:    "",
		ColorDim:       "",
		ColorHeader:    "",
		ColorReset:     "",
	}
}
