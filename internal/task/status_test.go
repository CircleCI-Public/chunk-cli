package task

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func newStatusTestClient(t *testing.T, fake *fakes.FakeCircleCI) *circleci.Client {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	cl, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	return cl
}

func TestPipelineWebURL(t *testing.T) {
	t.Run("with workflow", func(t *testing.T) {
		url := PipelineWebURL("gh/org/repo", 42, "wf-abc")
		assert.Equal(t, url, "https://app.circleci.com/pipelines/gh/org/repo/42/workflows/wf-abc")
	})

	t.Run("without workflow", func(t *testing.T) {
		url := PipelineWebURL("gh/org/repo", 42, "")
		assert.Equal(t, url, "https://app.circleci.com/pipelines/gh/org/repo/42")
	})
}

func TestWorkflowTerminal(t *testing.T) {
	assert.Assert(t, WorkflowTerminal("success"))
	assert.Assert(t, WorkflowTerminal("failed"))
	assert.Assert(t, WorkflowTerminal("canceled"))
	assert.Assert(t, !WorkflowTerminal("running"))
	assert.Assert(t, !WorkflowTerminal("on_hold"))
}

func TestFetchStatus(t *testing.T) {
	fake := fakes.NewFakeCircleCI()
	fake.Pipeline = &fakes.Pipeline{
		ID:          "pipe-1",
		ProjectSlug: "gh/org/repo",
		Number:      7,
		State:       "created",
	}
	fake.PipelineWorkflows = []fakes.Workflow{
		{ID: "wf-other", PipelineID: "pipe-1", Name: "other", Status: "success"},
		{ID: "wf-task", PipelineID: "pipe-1", Name: "chunk-task", Status: "running"},
	}
	fake.WorkflowJobs = map[string][]fakes.WorkflowJob{
		"wf-task": {{Name: "run-agent", Status: "running"}},
	}

	client := newStatusTestClient(t, fake)
	status, err := FetchStatus(context.Background(), client, "pipe-1")
	assert.NilError(t, err)
	assert.Equal(t, status.PipelineID, "pipe-1")
	assert.Equal(t, status.PipelineNumber, 7)
	assert.Equal(t, len(status.Workflows), 2)
	assert.Equal(t, status.Workflows[0].Name, "chunk-task")
	assert.Equal(t, status.Workflows[0].Jobs[0].Name, "run-agent")
	assert.Equal(t, status.WebURL, "https://app.circleci.com/pipelines/gh/org/repo/7/workflows/wf-task")
	assert.Assert(t, !status.AllTerminal())
}

func TestWatchStatusSuccess(t *testing.T) {
	fake := fakes.NewFakeCircleCI()
	fake.Pipeline = &fakes.Pipeline{
		ID:          "pipe-1",
		ProjectSlug: "gh/org/repo",
		Number:      1,
		State:       "created",
	}
	fake.PipelineWorkflows = []fakes.Workflow{
		{ID: "wf-1", PipelineID: "pipe-1", Name: "chunk-task", Status: "running"},
	}
	fake.WorkflowJobs = map[string][]fakes.WorkflowJob{
		"wf-1": {{Name: "run-agent", Status: "running"}},
	}

	client := newStatusTestClient(t, fake)

	done := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		fake.SetPipelineWorkflows([]fakes.Workflow{
			{ID: "wf-1", PipelineID: "pipe-1", Name: "chunk-task", Status: "success"},
		})
		fake.SetWorkflowJobs("wf-1", []fakes.WorkflowJob{{Name: "run-agent", Status: "success"}})
		close(done)
	}()

	status, err := WatchStatus(context.Background(), client, "pipe-1", WatchOptions{
		Interval: 20 * time.Millisecond,
		Timeout:  2 * time.Second,
	})
	assert.NilError(t, err)
	assert.Equal(t, status.Workflows[0].Status, "success")
	<-done
}

func TestFetchStatusNoWorkflows(t *testing.T) {
	fake := fakes.NewFakeCircleCI()
	fake.Pipeline = &fakes.Pipeline{
		ID:          "pipe-1",
		ProjectSlug: "gh/org/repo",
		Number:      3,
		State:       "created",
	}
	fake.PipelineWorkflows = []fakes.Workflow{}

	client := newStatusTestClient(t, fake)
	status, err := FetchStatus(context.Background(), client, "pipe-1")
	assert.NilError(t, err)
	assert.Equal(t, status.PipelineID, "pipe-1")
	assert.Equal(t, len(status.Workflows), 0)
	assert.Equal(t, status.WebURL, "https://app.circleci.com/pipelines/gh/org/repo/3")
	assert.Assert(t, !status.AllTerminal())
}

func TestWatchStatusTimeout(t *testing.T) {
	fake := fakes.NewFakeCircleCI()
	fake.Pipeline = &fakes.Pipeline{
		ID:          "pipe-1",
		ProjectSlug: "gh/org/repo",
		Number:      1,
		State:       "created",
	}
	fake.PipelineWorkflows = []fakes.Workflow{
		{ID: "wf-1", PipelineID: "pipe-1", Name: "chunk-task", Status: "running"},
	}
	fake.WorkflowJobs = map[string][]fakes.WorkflowJob{
		"wf-1": {{Name: "run-agent", Status: "running"}},
	}

	client := newStatusTestClient(t, fake)
	_, err := WatchStatus(context.Background(), client, "pipe-1", WatchOptions{
		Interval: 20 * time.Millisecond,
		Timeout:  50 * time.Millisecond,
	})
	assert.Assert(t, err != nil)
	assert.ErrorContains(t, err, "timed out waiting for workflow completion")
}

func TestWatchStatusFailed(t *testing.T) {
	fake := fakes.NewFakeCircleCI()
	fake.Pipeline = &fakes.Pipeline{
		ID:          "pipe-1",
		ProjectSlug: "gh/org/repo",
		Number:      1,
		State:       "created",
	}
	fake.PipelineWorkflows = []fakes.Workflow{
		{ID: "wf-1", PipelineID: "pipe-1", Name: "chunk-task", Status: "failed"},
	}
	fake.WorkflowJobs = map[string][]fakes.WorkflowJob{
		"wf-1": {{Name: "run-agent", Status: "failed"}},
	}

	client := newStatusTestClient(t, fake)
	_, err := WatchStatus(context.Background(), client, "pipe-1", WatchOptions{
		Interval: 20 * time.Millisecond,
		Timeout:  1 * time.Second,
	})
	assert.Assert(t, err != nil)
}
