package task

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

const taskWorkflowName = "chunk-task"

type JobStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type WorkflowStatus struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Status string      `json:"status"`
	Jobs   []JobStatus `json:"jobs,omitempty"`
}

type RunStatus struct {
	PipelineID     string           `json:"pipelineId"`
	PipelineNumber int              `json:"pipelineNumber"`
	PipelineState  string           `json:"pipelineState"`
	ProjectSlug    string           `json:"projectSlug"`
	WebURL         string           `json:"webUrl"`
	Workflows      []WorkflowStatus `json:"workflows"`
}

type WatchOptions struct {
	Interval time.Duration
	Timeout  time.Duration
	OnUpdate func(RunStatus)
}

func PipelineWebURL(projectSlug string, pipelineNumber int, workflowID string) string {
	base := fmt.Sprintf("https://app.circleci.com/pipelines/%s/%d", projectSlug, pipelineNumber)
	if workflowID == "" {
		return base
	}
	return base + "/workflows/" + workflowID
}

func WorkflowTerminal(status string) bool {
	switch status {
	case "success", "failed", "error", "canceled", "unauthorized":
		return true
	default:
		return false
	}
}

func (s RunStatus) AllTerminal() bool {
	if len(s.Workflows) == 0 {
		return false
	}
	for _, wf := range s.Workflows {
		if !WorkflowTerminal(wf.Status) {
			return false
		}
	}
	return true
}

func (s RunStatus) AnyFailed() bool {
	for _, wf := range s.Workflows {
		switch wf.Status {
		case "failed", "error", "canceled", "unauthorized":
			return true
		}
	}
	return false
}

func FetchStatus(ctx context.Context, client *circleci.Client, pipelineID string) (RunStatus, error) {
	pipe, err := client.GetPipeline(ctx, pipelineID)
	if err != nil {
		return RunStatus{}, fmt.Errorf("fetch pipeline: %w", err)
	}

	workflows, err := client.ListPipelineWorkflows(ctx, pipelineID)
	if err != nil {
		return RunStatus{}, fmt.Errorf("fetch workflows: %w", err)
	}
	sortWorkflows(workflows)

	status := RunStatus{
		PipelineID:     pipe.ID,
		PipelineNumber: pipe.Number,
		PipelineState:  pipe.State,
		ProjectSlug:    pipe.ProjectSlug,
		Workflows:      make([]WorkflowStatus, 0, len(workflows)),
	}

	primaryWorkflowID := ""
	for _, wf := range workflows {
		jobs, err := client.ListWorkflowJobs(ctx, wf.ID)
		if err != nil {
			return RunStatus{}, fmt.Errorf("fetch jobs for workflow %s: %w", wf.ID, err)
		}
		wfStatus := WorkflowStatus{
			ID:     wf.ID,
			Name:   wf.Name,
			Status: wf.Status,
		}
		for _, job := range jobs {
			wfStatus.Jobs = append(wfStatus.Jobs, JobStatus{
				Name:   job.Name,
				Status: job.Status,
			})
		}
		status.Workflows = append(status.Workflows, wfStatus)
		if primaryWorkflowID == "" {
			primaryWorkflowID = wf.ID
		}
	}

	status.WebURL = PipelineWebURL(pipe.ProjectSlug, pipe.Number, primaryWorkflowID)
	return status, nil
}

func RunWebURL(ctx context.Context, client *circleci.Client, pipelineID string) (string, error) {
	pipe, err := client.GetPipeline(ctx, pipelineID)
	if err != nil {
		return "", fmt.Errorf("fetch pipeline: %w", err)
	}
	workflows, err := client.ListPipelineWorkflows(ctx, pipelineID)
	if err != nil {
		return "", fmt.Errorf("fetch workflows: %w", err)
	}
	sortWorkflows(workflows)
	workflowID := ""
	if len(workflows) > 0 {
		workflowID = workflows[0].ID
	}
	return PipelineWebURL(pipe.ProjectSlug, pipe.Number, workflowID), nil
}

func WatchStatus(ctx context.Context, client *circleci.Client, pipelineID string, opts WatchOptions) (RunStatus, error) {
	if opts.Interval <= 0 {
		opts.Interval = 5 * time.Second
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Minute
	}

	deadline := time.Now().Add(opts.Timeout)

	for {
		status, err := FetchStatus(ctx, client, pipelineID)
		if err != nil {
			return RunStatus{}, err
		}
		if opts.OnUpdate != nil {
			opts.OnUpdate(status)
		}
		if status.AllTerminal() {
			if status.AnyFailed() {
				return status, fmt.Errorf("workflow failed")
			}
			return status, nil
		}
		if time.Now().After(deadline) {
			return status, fmt.Errorf("timed out waiting for workflow completion")
		}

		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-time.After(opts.Interval):
		}
	}
}

func sortWorkflows(workflows []circleci.Workflow) {
	sort.SliceStable(workflows, func(i, j int) bool {
		iTask := strings.EqualFold(workflows[i].Name, taskWorkflowName)
		jTask := strings.EqualFold(workflows[j].Name, taskWorkflowName)
		if iTask != jTask {
			return iTask
		}
		return workflows[i].Name < workflows[j].Name
	})
}
