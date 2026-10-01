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

func TestRunDistributedReportsEachCommandToItsWorker(t *testing.T) {
	commands := []config.Command{{Name: "one"}, {Name: "two"}, {Name: "three"}, {Name: "four"}}
	workers := make(chan int, 2)
	workers <- 1
	workers <- 2

	var mu sync.Mutex
	byWorker := map[int][]string{}
	var shared []string

	result := RunDistributed(context.Background(), commands, DistributedRunOptions[int]{
		Parallelism: 2,
		Acquire:     func(context.Context) (int, error) { return <-workers, nil },
		Release:     func(worker int) { workers <- worker },
		WorkerName:  func(worker int) string { return fmt.Sprintf("sidecar-%d", worker) },
		WorkerStatus: func(worker int) iostream.StatusFunc {
			return func(_ iostream.Level, message string) {
				mu.Lock()
				defer mu.Unlock()
				byWorker[worker] = append(byWorker[worker], message)
			}
		},
		Run: func(_ context.Context, worker int, command config.Command, status iostream.StatusFunc, _ iostream.Streams) DistributedJobResult {
			status(iostream.LevelDone, fmt.Sprintf("%s done on sidecar-%d", command.Name, worker))
			return DistributedJobResult{Passed: 1}
		},
		Status: func(_ iostream.Level, message string) {
			mu.Lock()
			defer mu.Unlock()
			shared = append(shared, message)
		},
	})

	assert.NilError(t, result.Err)
	assert.Equal(t, result.Passed, 4)

	mu.Lock()
	defer mu.Unlock()
	// Every command reported through the worker that ran it, and nothing
	// reached the run-wide reporter: a line there names no sidecar, so a reader
	// matching on sidecar alone cannot place it.
	assert.Equal(t, len(shared), 0, "shared: %v", shared)
	var reported int
	for worker, messages := range byWorker {
		for _, m := range messages {
			reported++
			assert.Assert(t, strings.Contains(m, fmt.Sprintf("sidecar-%d", worker)),
				"worker %d got %q", worker, m)
		}
	}
	// One "running on" line plus one done line per command.
	assert.Equal(t, reported, 8)
}

func TestRunDistributedReportsWhileJobsRun(t *testing.T) {
	// A done line held until every worker finished left a pooled run silent for
	// its whole duration.
	release := make(chan struct{})
	reported := make(chan string, 2)
	finished := make(chan DistributedRunResult, 1)

	go func() {
		finished <- RunDistributed(context.Background(), []config.Command{{Name: "slow"}, {Name: "quick"}}, DistributedRunOptions[int]{
			Parallelism: 2,
			Acquire:     func(context.Context) (int, error) { return 1, nil },
			Release:     func(int) {},
			WorkerStatus: func(int) iostream.StatusFunc {
				return func(level iostream.Level, message string) {
					if level == iostream.LevelDone {
						reported <- message
					}
				}
			},
			Run: func(_ context.Context, _ int, command config.Command, status iostream.StatusFunc, _ iostream.Streams) DistributedJobResult {
				if command.Name == "slow" {
					<-release
				}
				status(iostream.LevelDone, command.Name)
				return DistributedJobResult{Passed: 1}
			},
		})
	}()

	// The quick command must report before the slow one is allowed to finish.
	assert.Equal(t, <-reported, "quick")
	close(release)
	assert.Equal(t, <-reported, "slow")
	assert.NilError(t, (<-finished).Err)
}

func TestRunDistributedFallsBackToRunStatus(t *testing.T) {
	var got []string
	result := RunDistributed(context.Background(), []config.Command{{Name: "test"}}, DistributedRunOptions[int]{
		Parallelism: 1,
		Acquire:     func(context.Context) (int, error) { return 1, nil },
		Release:     func(int) {},
		Run: func(_ context.Context, _ int, command config.Command, status iostream.StatusFunc, _ iostream.Streams) DistributedJobResult {
			status(iostream.LevelDone, command.Name)
			return DistributedJobResult{Passed: 1}
		},
		Status: func(_ iostream.Level, message string) { got = append(got, message) },
	})

	assert.NilError(t, result.Err)
	assert.DeepEqual(t, got, []string{"running on worker: test", "test"})
}

// A WorkerStatus that has nothing to record for a worker, as the pool's does
// when the project has no event log, must not leave that worker's commands
// unreported.
func TestRunDistributedFallsBackWhenWorkerStatusIsNil(t *testing.T) {
	var got []string
	result := RunDistributed(context.Background(), []config.Command{{Name: "test"}}, DistributedRunOptions[int]{
		Parallelism:  1,
		Acquire:      func(context.Context) (int, error) { return 1, nil },
		Release:      func(int) {},
		WorkerStatus: func(int) iostream.StatusFunc { return nil },
		Run: func(_ context.Context, _ int, command config.Command, status iostream.StatusFunc, _ iostream.Streams) DistributedJobResult {
			status(iostream.LevelDone, command.Name)
			return DistributedJobResult{Passed: 1}
		},
		Status: func(_ iostream.Level, message string) { got = append(got, message) },
	})

	assert.NilError(t, result.Err)
	assert.DeepEqual(t, got, []string{"running on worker: test", "test"})
}
