package chunkd

import "time"

// CommandReg is the registration a process sends after submitting a remote
// command, so the daemon can stream and buffer that command's output. The
// submitting process may exit immediately afterwards — that is the whole point,
// since most remote commands are run by a hook that exits as soon as the command
// finishes.
type CommandReg struct {
	CommandID   string    `json:"command_id"`
	SidecarID   string    `json:"sidecar_id"`
	ProjectRoot string    `json:"project_root"`
	Op          string    `json:"op"`
	Name        string    `json:"name"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// OutputChunk is one response to an output read.
type OutputChunk struct {
	// Data is raw command output, exactly as the remote command wrote it —
	// interleaved stdout and stderr, ANSI and carriage returns intact.
	Data []byte `json:"data"`
	// NextOffset is the offset to pass on the following read.
	NextOffset int64 `json:"next_offset"`
	Running    bool  `json:"running"`
	ExitCode   *int  `json:"exit_code,omitempty"`
	// Truncated reports that output before the returned data was evicted and is
	// gone. Saying so is the difference between showing a partial run and
	// showing a partial run that looks whole.
	Truncated bool `json:"truncated"`
	// Found is false when the daemon knows nothing about the command.
	Found bool `json:"found"`
	// Error explains why streaming stopped early, when it did. Without it a
	// failed stream is indistinguishable from a command that produced no output,
	// which sends the reader looking for a bug in their own command.
	Error string `json:"error,omitempty"`
}
