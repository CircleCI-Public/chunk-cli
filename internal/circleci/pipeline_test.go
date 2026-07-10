package circleci

import (
	"context"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestGetPipeline(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.Pipeline = &fakes.Pipeline{
			ID:          "pipe-1",
			ProjectSlug: "gh/org/repo",
			Number:      42,
			State:       "created",
		}
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		pipe, err := client.GetPipeline(context.Background(), "pipe-1")
		assert.NilError(t, err)
		assert.Equal(t, pipe.ID, "pipe-1")
		assert.Equal(t, pipe.ProjectSlug, "gh/org/repo")
		assert.Equal(t, pipe.Number, 42)
		assert.Equal(t, pipe.State, "created")

		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		assert.Equal(t, last.URL.Path, "/api/v2/pipeline/pipe-1")
	})

	t.Run("not found", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.PipelineStatusCode = 404
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		_, err := client.GetPipeline(context.Background(), "missing")
		assert.Assert(t, err != nil)
	})
}

func TestListPipelineWorkflows(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.PipelineWorkflows = []fakes.Workflow{
			{ID: "wf-1", PipelineID: "pipe-1", Name: "chunk-task", Status: "running"},
			{ID: "wf-2", PipelineID: "pipe-1", Name: "other", Status: "success"},
		}
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		wfs, err := client.ListPipelineWorkflows(context.Background(), "pipe-1")
		assert.NilError(t, err)
		assert.Equal(t, len(wfs), 2)
		assert.Equal(t, wfs[0].Name, "chunk-task")
		assert.Equal(t, wfs[0].Status, "running")

		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		assert.Equal(t, last.URL.Path, "/api/v2/pipeline/pipe-1/workflow")
	})
}

func TestListWorkflowJobs(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.WorkflowJobs = map[string][]fakes.WorkflowJob{
			"wf-1": {
				{Name: "build", Status: "success"},
				{Name: "test", Status: "failed"},
			},
		}
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		jobs, err := client.ListWorkflowJobs(context.Background(), "wf-1")
		assert.NilError(t, err)
		assert.Equal(t, len(jobs), 2)
		assert.Equal(t, jobs[1].Name, "test")
		assert.Equal(t, jobs[1].Status, "failed")

		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		assert.Equal(t, last.URL.Path, "/api/v2/workflow/wf-1/job")
	})
}
