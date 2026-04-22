package iostream

// Level indicates the kind of status message.
type Level int

// Status levels for progress reporting.
const (
	LevelStep Level = iota // numbered step heading (e.g. "Step 1/3: Discovering...")
	LevelInfo              // dim informational detail
	LevelWarn              // yellow warning
	LevelDone              // green success/completion
)

// StatusFunc is a callback for reporting progress from business logic.
// Business logic calls this instead of importing ui directly.
type StatusFunc func(level Level, msg string)
