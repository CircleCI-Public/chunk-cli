package circleci

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// newSubmitStreamFake wires the shared SSE fake and returns a client plus the
// fake, so a test can submit and stream as separate steps.
func newSubmitStreamFake(t *testing.T, resp *fakes.ExecResponse) (*Client, *fakes.FakeCircleCI) {
	t.Helper()
	previous := streamRetryBase
	streamRetryBase = time.Millisecond
	t.Cleanup(func() { streamRetryBase = previous })

	fake := fakes.NewFakeCircleCI()
	fake.ExecResponse = resp
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return newTestClient(t, srv.URL), fake
}

// The whole point of the split: the command ID must be available before any
// output is consumed, so it can be handed to something else while the command is
// still running.
func TestSubmitExecReturnsIDBeforeStreaming(t *testing.T) {
	client, _ := newSubmitStreamFake(t, &fakes.ExecResponse{
		CommandID: "cmd-abc",
		Stdout:    "hello\n",
		ExitCode:  0,
	})

	commandID, err := client.SubmitExec(context.Background(), "sb-1", "sh", []string{"-c", "echo hello"}, nil)
	assert.NilError(t, err)
	assert.Check(t, commandID != "", "SubmitExec must return a usable command ID")
}

func TestStreamOutputConsumesSubmittedCommand(t *testing.T) {
	client, _ := newSubmitStreamFake(t, &fakes.ExecResponse{
		CommandID: "cmd-abc",
		Stdout:    "out\n",
		Stderr:    "err\n",
		ExitCode:  3,
	})

	commandID, err := client.SubmitExec(context.Background(), "sb-1", "sh", nil, nil)
	assert.NilError(t, err)

	var stdout, stderr bytes.Buffer
	onOutput := func(stream string, data []byte) {
		if stream == StreamStderr {
			stderr.Write(data)
		} else {
			stdout.Write(data)
		}
	}
	resp, err := client.StreamOutput(context.Background(), commandID, "", onOutput)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(resp.ExitCode, 3))
	assert.Check(t, cmp.Equal(stdout.String(), "out\n"))
	assert.Check(t, cmp.Equal(stderr.String(), "err\n"))
	assert.Check(t, cmp.Equal(resp.CommandID, commandID))
}

// Attaching after the command has already finished is the replay case, and it is
// what makes reading back an old run work at all. It must behave the same as
// attaching while it runs.
func TestStreamOutputReplaysAnAlreadyExitedCommand(t *testing.T) {
	client, _ := newSubmitStreamFake(t, &fakes.ExecResponse{
		CommandID: "cmd-abc",
		Stdout:    "finished output\n",
		ExitCode:  0,
	})

	commandID, err := client.SubmitExec(context.Background(), "sb-1", "sh", nil, nil)
	assert.NilError(t, err)

	// Drain it once, as the submitting process would.
	first, err := client.StreamOutput(context.Background(), commandID, "", nil)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(first.Stdout, "finished output\n"))

	// Then attach again from the beginning, as a dashboard opened later would.
	var replayed bytes.Buffer
	second, err := client.StreamOutput(context.Background(), commandID, "", func(_ string, data []byte) {
		replayed.Write(data)
	})
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(second.ExitCode, 0))
	assert.Check(t, cmp.Equal(replayed.String(), "finished output\n"))
}

// StreamOutput accumulates into the response only when no callback is given —
// the callback form leaves them empty because output can be arbitrarily large.
func TestStreamOutputAccumulatesWithoutCallback(t *testing.T) {
	client, _ := newSubmitStreamFake(t, &fakes.ExecResponse{
		CommandID: "cmd-abc",
		Stdout:    "buffered\n",
		ExitCode:  0,
	})

	commandID, err := client.SubmitExec(context.Background(), "sb-1", "sh", nil, nil)
	assert.NilError(t, err)

	resp, err := client.StreamOutput(context.Background(), commandID, "", nil)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(resp.Stdout, "buffered\n"))
}

// Exec must remain exactly the composition of the two, since every existing
// caller still uses it.
func TestExecStillComposesSubmitAndStream(t *testing.T) {
	client, _ := newSubmitStreamFake(t, &fakes.ExecResponse{
		CommandID: "cmd-abc",
		Stdout:    "composed\n",
		ExitCode:  7,
	})

	var got bytes.Buffer
	resp, err := client.Exec(context.Background(), "sb-1", "sh", nil, nil, func(_ string, data []byte) {
		got.Write(data)
	})
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(resp.ExitCode, 7))
	assert.Check(t, cmp.Equal(got.String(), "composed\n"))
	assert.Check(t, resp.CommandID != "")
}

func TestStreamOutputHonoursCancellation(t *testing.T) {
	client, _ := newSubmitStreamFake(t, &fakes.ExecResponse{CommandID: "cmd-abc", Stdout: "x\n", ExitCode: 0})

	commandID, err := client.SubmitExec(context.Background(), "sb-1", "sh", nil, nil)
	assert.NilError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.StreamOutput(ctx, commandID, "", nil)
	assert.Check(t, err != nil, "a cancelled context must abort the stream")
}
