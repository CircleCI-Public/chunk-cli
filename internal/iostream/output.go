package iostream

// Stream names for a command's output, as OutputFn receives them. They are the
// two POSIX output streams, not anything backend-specific.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// OutputFn receives a run of raw output bytes from one stream (StreamStdout or
// StreamStderr), exactly as the command produced them. It is the streaming
// callback every sidecar backend and command runner reports output through, so
// it lives here in the neutral iostream package rather than tied to any one
// backend.
type OutputFn func(stream string, data []byte)
