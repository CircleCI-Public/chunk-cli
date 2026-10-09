package watchd

import "time"

// TaskState is a validation task as reported to a caller.
type TaskState struct {
	ID string `json:"id"`
	// ProjectRoot is the key the task is filed under — the root of the repo,
	// which may sit above the directory the run was actually asked for. See
	// taskStore.projectKey.
	ProjectRoot string    `json:"project_root"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	Running     bool      `json:"running"`
	// ExitCode is the validate run's exit status. Meaningful only once the task
	// has finished.
	ExitCode int `json:"exit_code"`
	// Output is what the run printed, held for whoever collects the result.
	// Capped at maxTaskOutput, keeping the tail.
	Output string `json:"output,omitempty"`
	// Stale reports that the working tree changed between the run starting and
	// its result being read, so the result describes code that is no longer on
	// disk. For a live-tree run ExitCode and Output are cleared when it is set: a
	// stale task is reported so that the discard is visible, and carrying the
	// verdict would invite it to be read as one. For a snapshot run they are
	// kept — see Snapshot, where the verdict outlives the tree moving.
	//
	// The tree most often moved because of the run itself — output written,
	// goldens regenerated, a lockfile touched. Reporting the discard is what
	// lets that be diagnosed instead of looking like no run happened.
	//
	// It is not a fix for the cause. A mid-run edit by the developer and a file
	// written by the run are the same event as far as a content digest is
	// concerned, so nothing here can tell them apart, and relaxing the
	// comparison to let artifacts through would let a real edit through with
	// them — trading a silence for a false pass, which is the one direction this
	// store must not fail in. Actually keeping the result means stopping the
	// tree from moving under the run, which is a matter of where the run
	// happens rather than how its result is judged.
	Stale bool `json:"stale"`
	// Snapshot reports that the run validated a checked-out copy of the tree
	// rather than the tree itself. Such a result is exact about the state it
	// ran against whatever happened afterwards, so it is reported even when
	// stale — qualified rather than thrown away.
	Snapshot bool `json:"snapshot,omitempty"`
	// DeliveredAt records when this result was handed to a caller. A stamped
	// task is never reported again; an unstamped one is still owed to somebody.
	//
	// It exists so that handing a result over and forgetting it are two steps
	// rather than one. See taskStore.collect.
	DeliveredAt time.Time `json:"delivered_at,omitempty"`
}

// Passed reports whether a finished task validated the tree successfully.
//
// A stale live-tree task never passes, whatever its exit code says. Its verdict
// is stripped when it is reported, which leaves ExitCode at zero — so without
// the Stale check a discarded run would read here as a clean pass, which is
// exactly the claim discarding it exists to avoid making.
//
// A stale snapshot task can still pass. It ran against a copy that could not
// move, so its exit code remains exactly true about the state it was handed;
// what staleness says there is that the state has been overtaken, not that the
// verdict is unreliable. Callers are expected to report that qualification —
// see printResults.
func (t TaskState) Passed() bool {
	return !t.Running && t.ExitCode == 0 && (!t.Stale || t.Snapshot)
}
