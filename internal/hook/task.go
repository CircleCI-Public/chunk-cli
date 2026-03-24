package hook

// TaskCheckFlags holds parsed flags for task check.
type TaskCheckFlags struct {
	Name         string
	Instructions string
	Schema       string
	Always       bool
	Staged       bool
	On           string
	Trigger      string
	Matcher      string
	Limit        int
}

// RunTaskCheck checks a task result. When not enabled, exits 0.
func RunTaskCheck(cfg *ResolvedConfig, flags TaskCheckFlags) error {
	if !IsEnabled(flags.Name) {
		return nil
	}

	// Full implementation would read task sentinel and enforce result.
	return nil
}
