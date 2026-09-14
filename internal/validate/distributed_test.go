package validate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func TestRunDistributedUsesSingleWorkerInCommandOrder(t *testing.T) {
	commands := []config.Command{{Name: "test"}, {Name: "lint"}, {Name: "format"}}
	var ran []string
	acquired := 0
	released := 0

	result := RunDistributed(context.Background(), commands, DistributedRunOptions[int]{
		Parallelism: 1,
		Acquire: func(context.Context) (int, error) {
			acquired++
			return 7, nil
		},
		Release: func(worker int) {
			assert.Equal(t, worker, 7)
			released++
		},
		WorkerName: func(worker int) string { return fmt.Sprintf("sidecar-%d", worker) },
		Run: func(_ context.Context, worker int, command config.Command, _ iostream.StatusFunc, _ iostream.Streams) DistributedJobResult {
			assert.Equal(t, worker, 7)
			ran = append(ran, command.Name)
			return DistributedJobResult{Passed: 1}
		},
	})

	assert.NilError(t, result.Err)
	assert.Equal(t, result.Passed, 3)
	assert.DeepEqual(t, ran, []string{"test", "lint", "format"})
	assert.Equal(t, acquired, 1)
	assert.Equal(t, released, 1)
}

func TestRunDistributedQueuesWorkAcrossWorkers(t *testing.T) {
	commands := []config.Command{{Name: "one"}, {Name: "two"}, {Name: "three"}, {Name: "four"}}
	workers := make(chan int, 2)
	workers <- 1
	workers <- 2
	var mu sync.Mutex
	seen := map[int]int{}
	started := make(chan struct{}, 2)
	bothStarted := make(chan struct{})
	go func() {
		<-started
		<-started
		close(bothStarted)
	}()

	result := RunDistributed(context.Background(), commands, DistributedRunOptions[int]{
		Parallelism: 2,
		Acquire: func(context.Context) (int, error) {
			return <-workers, nil
		},
		Release:    func(worker int) { workers <- worker },
		WorkerName: func(worker int) string { return fmt.Sprintf("sidecar-%d", worker) },
		Run: func(_ context.Context, worker int, command config.Command, _ iostream.StatusFunc, streams iostream.Streams) DistributedJobResult {
			started <- struct{}{}
			<-bothStarted
			mu.Lock()
			seen[worker]++
			mu.Unlock()
			_, _ = fmt.Fprint(streams.Out, command.Name)
			return DistributedJobResult{Passed: 1}
		},
	})

	assert.NilError(t, result.Err)
	assert.Equal(t, result.Passed, 4)
	assert.Assert(t, seen[1] > 0)
	assert.Assert(t, seen[2] > 0)
	assert.Equal(t, len(result.Output), 4)
	for index, output := range result.Output {
		assert.Equal(t, output.Command.Name, commands[index].Name)
		assert.Equal(t, output.Stdout, commands[index].Name)
	}
}

func TestRunDistributedAggregatesErrors(t *testing.T) {
	commandErr := errors.New("command failed")
	commands := []config.Command{{Name: "pass"}, {Name: "fail"}}

	result := RunDistributed(context.Background(), commands, DistributedRunOptions[int]{
		Parallelism: 1,
		Acquire:     func(context.Context) (int, error) { return 1, nil },
		Release:     func(int) {},
		Run: func(_ context.Context, _ int, command config.Command, _ iostream.StatusFunc, _ iostream.Streams) DistributedJobResult {
			if command.Name == "pass" {
				return DistributedJobResult{Passed: 1}
			}
			return DistributedJobResult{Err: commandErr}
		},
	})

	assert.Equal(t, result.Passed, 1)
	assert.ErrorContains(t, result.Err, commandErr.Error())
}

func TestRunDistributedReportsAcquireFailure(t *testing.T) {
	wantErr := errors.New("no worker")
	result := RunDistributed(context.Background(), []config.Command{{Name: "test"}}, DistributedRunOptions[int]{
		Parallelism: 1,
		Acquire:     func(context.Context) (int, error) { return 0, wantErr },
		Release:     func(int) {},
		Run: func(context.Context, int, config.Command, iostream.StatusFunc, iostream.Streams) DistributedJobResult {
			return DistributedJobResult{}
		},
	})

	assert.ErrorContains(t, result.Err, "acquire worker: no worker")
}

func TestRunDistributedStreamsSingleWorkerOutput(t *testing.T) {
	var stdout strings.Builder
	result := RunDistributed(context.Background(), []config.Command{{Name: "test"}}, DistributedRunOptions[int]{
		Parallelism: 1,
		Acquire:     func(context.Context) (int, error) { return 1, nil },
		Release:     func(int) {},
		Run: func(_ context.Context, _ int, _ config.Command, _ iostream.StatusFunc, streams iostream.Streams) DistributedJobResult {
			_, _ = fmt.Fprint(streams.Out, "live")
			return DistributedJobResult{Passed: 1}
		},
		Streams: iostream.Streams{Out: &stdout},
	})

	assert.NilError(t, result.Err)
	assert.Equal(t, stdout.String(), "live")
	assert.Equal(t, len(result.Output), 0)
}
