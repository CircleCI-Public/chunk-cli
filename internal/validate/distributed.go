package validate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// DistributedJobResult reports the outcome of one remotely scheduled command.
type DistributedJobResult struct {
	Passed int
	Err    error
}

// DistributedRunOutput is buffered output from one concurrently run command.
type DistributedRunOutput struct {
	Command config.Command
	Stdout  string
	Stderr  string
}

// DistributedRunResult aggregates remotely scheduled validation commands.
type DistributedRunResult struct {
	Passed int
	Output []DistributedRunOutput
	Err    error
}

// DistributedRunOptions supplies resource management and command execution.
// The scheduler owns jobs; Acquire and Release adapt any concrete worker pool.
type DistributedRunOptions[T any] struct {
	Parallelism int
	Acquire     func(context.Context) (T, error)
	Release     func(T)
	WorkerName  func(T) string
	Run         func(context.Context, T, config.Command, iostream.StatusFunc, iostream.Streams) DistributedJobResult
	Status      iostream.StatusFunc
	Streams     iostream.Streams
}

type distributedJob struct {
	index   int
	command config.Command
}

type distributedJobResult struct {
	index      int
	command    config.Command
	workerName string
	stdout     string
	stderr     string
	result     DistributedJobResult
}

// RunDistributed schedules commands in configuration order onto the next
// available worker. A worker is held for the run and consumes commands from a
// shared queue, so faster workers naturally process more jobs.
func RunDistributed[T any](ctx context.Context, commands []config.Command, opts DistributedRunOptions[T]) DistributedRunResult {
	if len(commands) == 0 {
		return DistributedRunResult{}
	}
	if opts.Parallelism < 1 {
		return DistributedRunResult{Err: errors.New("distributed validation: parallelism must be positive")}
	}
	if opts.Acquire == nil || opts.Release == nil || opts.Run == nil {
		return DistributedRunResult{Err: errors.New("distributed validation: incomplete worker configuration")}
	}

	parallelism := min(opts.Parallelism, len(commands))
	jobs := make(chan distributedJob, len(commands))
	for index, command := range commands {
		jobs <- distributedJob{index: index, command: command}
	}
	close(jobs)

	results := make(chan distributedJobResult, len(commands)+parallelism)
	var statusMu sync.Mutex
	status := func(level iostream.Level, message string) {
		if opts.Status == nil {
			return
		}
		statusMu.Lock()
		defer statusMu.Unlock()
		opts.Status(level, message)
	}

	var workers sync.WaitGroup
	for range parallelism {
		workers.Add(1)
		go func() {
			defer workers.Done()
			worker, err := opts.Acquire(ctx)
			if err != nil {
				results <- distributedJobResult{index: -1, result: DistributedJobResult{Err: fmt.Errorf("acquire worker: %w", err)}}
				return
			}
			defer opts.Release(worker)

			workerName := "worker"
			if opts.WorkerName != nil {
				workerName = opts.WorkerName(worker)
			}
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					status(iostream.LevelInfo, fmt.Sprintf("running on %s: %s", workerName, job.command.Name))
					results <- runDistributedJob(ctx, worker, workerName, job, parallelism > 1, opts)
				}
			}
		}()
	}

	workers.Wait()
	close(results)
	return collectDistributedResults(commands, results, ctx.Err(), status)
}

func runDistributedJob[T any](ctx context.Context, worker T, workerName string, job distributedJob, buffered bool, opts DistributedRunOptions[T]) distributedJobResult {
	streams := opts.Streams
	status := opts.Status
	if status == nil {
		status = func(iostream.Level, string) {}
	}
	var stdout, stderr strings.Builder
	if buffered {
		streams = iostream.Streams{Out: &stdout, Err: &stderr}
		status = func(iostream.Level, string) {}
	}
	result := opts.Run(ctx, worker, job.command, status, streams)
	return distributedJobResult{
		index:      job.index,
		command:    job.command,
		workerName: workerName,
		stdout:     stdout.String(),
		stderr:     stderr.String(),
		result:     result,
	}
}

func collectDistributedResults(commands []config.Command, results <-chan distributedJobResult, contextErr error, status iostream.StatusFunc) DistributedRunResult {
	ordered := make([]distributedJobResult, len(commands))
	completed := make([]bool, len(commands))
	completedCount := 0
	var result DistributedRunResult
	for job := range results {
		if job.index < 0 {
			result.Err = errors.Join(result.Err, job.result.Err)
			continue
		}
		ordered[job.index] = job
		completed[job.index] = true
		completedCount++
	}

	for index, job := range ordered {
		if !completed[index] {
			continue
		}
		result.Passed += job.result.Passed
		result.Err = errors.Join(result.Err, job.result.Err)
		if job.stdout != "" || job.stderr != "" {
			result.Output = append(result.Output, DistributedRunOutput{Command: job.command, Stdout: job.stdout, Stderr: job.stderr})
		}
		if job.result.Err != nil {
			status(iostream.LevelWarn, fmt.Sprintf("%s failed on %s: %v", job.command.Name, job.workerName, job.result.Err))
		} else {
			status(iostream.LevelDone, fmt.Sprintf("%s passed on %s", job.command.Name, job.workerName))
		}
	}
	if contextErr != nil && completedCount < len(commands) {
		result.Err = errors.Join(result.Err, contextErr)
	}
	return result
}
