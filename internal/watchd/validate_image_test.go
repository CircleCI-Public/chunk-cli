package watchd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// imageRecorder wraps the fake CircleCI server and records the image of every
// sidecar create request it sees.
type imageRecorder struct {
	next   http.Handler
	mu     sync.Mutex
	images []string
}

func (r *imageRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodPost && req.URL.Path == "/api/v3/sidecar/instances" {
		body, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
		var env struct {
			Data struct {
				Attributes struct {
					Image string `json:"image"`
				} `json:"attributes"`
			} `json:"data"`
		}
		_ = json.Unmarshal(body, &env)
		r.mu.Lock()
		r.images = append(r.images, env.Data.Attributes.Image)
		r.mu.Unlock()
	}
	r.next.ServeHTTP(w, req)
}

func (r *imageRecorder) created() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.images...)
}

func writeProjectConfig(t *testing.T, dir, content string) {
	t.Helper()
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".chunk"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".chunk", "config.json"), []byte(content), 0o644))
}

func TestRunValidateNowUsesConfiguredSidecarImage(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{
			name:   "snapshot configured",
			config: `{"validation":{"sidecarImage":"snap-123"}}`,
			want:   "snap-123",
		},
		{
			name:   "no snapshot configured",
			config: `{}`,
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &imageRecorder{next: fakes.NewFakeCircleCI()}
			srv := httptest.NewServer(rec)
			defer srv.Close()

			root := t.TempDir()
			writeProjectConfig(t, root, tt.config)

			d := newTestDaemon()
			d.prov = newProvisioner(newTestClient(t, srv.URL))
			d.runner = func(context.Context, string, string, []string, []string, io.Writer, io.Writer) int { return 0 }

			resp := d.runValidateNow(context.Background(), ValidateRequest{
				Args:        []string{"validate", "--remote"},
				OrgID:       "org-1",
				ProjectRoot: root,
			}, nil)

			assert.Check(t, cmp.Equal(resp.ExitCode, 0))
			assert.Check(t, cmp.DeepEqual(rec.created(), []string{tt.want}))
		})
	}
}

// The config lives in WorkDir when .chunk sits below the git root, so the
// image is read from there rather than from ProjectRoot.
func TestRunValidateNowReadsSidecarImageFromWorkDir(t *testing.T) {
	rec := &imageRecorder{next: fakes.NewFakeCircleCI()}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	root := t.TempDir()
	workDir := filepath.Join(root, "sub")
	writeProjectConfig(t, workDir, `{"validation":{"sidecarImage":"snap-sub"}}`)

	d := newTestDaemon()
	d.prov = newProvisioner(newTestClient(t, srv.URL))
	d.runner = func(context.Context, string, string, []string, []string, io.Writer, io.Writer) int { return 0 }

	d.runValidateNow(context.Background(), ValidateRequest{
		Args:        []string{"validate", "--remote"},
		OrgID:       "org-1",
		ProjectRoot: root,
		WorkDir:     workDir,
	}, nil)

	assert.Check(t, cmp.DeepEqual(rec.created(), []string{"snap-sub"}))
}
