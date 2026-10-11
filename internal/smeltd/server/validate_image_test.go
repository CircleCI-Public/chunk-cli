package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/smeltdev/smelt/internal/smeltd"
	"github.com/smeltdev/smelt/internal/testing/fakes"
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
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".smelt"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".smelt", "config.json"), []byte(content), 0o644))
}

func TestRunValidateNowUsesConfiguredSidecarImage(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{
			name:   "snapshot configured",
			config: `{"factory":{"backend":"circleci"},"validation":{"sidecarImage":"snap-123"}}`,
			want:   "snap-123",
		},
		{
			name:   "no snapshot configured",
			config: `{"factory":{"backend":"circleci"}}`,
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

			resp := d.runValidateNow(context.Background(), smeltd.ValidateRequest{
				Args:        []string{"validate", "--remote"},
				OrgID:       "org-1",
				ProjectRoot: root,
			}, nil)

			assert.Check(t, cmp.Equal(resp.ExitCode, 0))
			assert.Check(t, cmp.DeepEqual(rec.created(), []string{tt.want}))
		})
	}
}

// The config lives in WorkDir when .smelt sits below the git root, so the
// image is read from there rather than from ProjectRoot.
func TestRunValidateNowReadsSidecarImageFromWorkDir(t *testing.T) {
	rec := &imageRecorder{next: fakes.NewFakeCircleCI()}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	root := t.TempDir()
	workDir := filepath.Join(root, "sub")
	writeProjectConfig(t, workDir, `{"factory":{"backend":"circleci"},"validation":{"sidecarImage":"snap-sub"}}`)

	d := newTestDaemon()
	d.prov = newProvisioner(newTestClient(t, srv.URL))
	d.runner = func(context.Context, string, string, []string, []string, io.Writer, io.Writer) int { return 0 }

	d.runValidateNow(context.Background(), smeltd.ValidateRequest{
		Args:        []string{"validate", "--remote"},
		OrgID:       "org-1",
		ProjectRoot: root,
		WorkDir:     workDir,
	}, nil)

	assert.Check(t, cmp.DeepEqual(rec.created(), []string{"snap-sub"}))
}

// rejectImage answers a sidecar create for one snapshot with a 404, as the API
// does for a snapshot that is gone or belongs to another org.
type rejectImage struct {
	next  http.Handler
	image string
}

func (r rejectImage) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodPost && req.URL.Path == "/api/v3/sidecar/instances" {
		body, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(r.image)) {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
	}
	r.next.ServeHTTP(w, req)
}

func runnerNeverCalled(t *testing.T) func(context.Context, string, string, []string, []string, io.Writer, io.Writer) int {
	return func(context.Context, string, string, []string, []string, io.Writer, io.Writer) int {
		t.Error("the validate run should not start")
		return 0
	}
}

// A config that cannot be loaded has not opted into CircleCI, so the daemon
// provisions nothing for it; the run itself reports the broken config.
func TestRunValidateNowDoesNotProvisionForAnUnreadableProjectConfig(t *testing.T) {
	rec := &imageRecorder{next: fakes.NewFakeCircleCI()}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	root := t.TempDir()
	writeProjectConfig(t, root, `{"validation": not json`)

	d := newTestDaemon()
	d.prov = newProvisioner(newTestClient(t, srv.URL))
	d.runner = func(context.Context, string, string, []string, []string, io.Writer, io.Writer) int { return 0 }

	resp := d.runValidateNow(context.Background(), smeltd.ValidateRequest{
		Args:        []string{"validate", "--remote"},
		OrgID:       "org-1",
		ProjectRoot: root,
	}, nil)

	assert.Check(t, cmp.Equal(resp.ExitCode, 0))
	assert.Check(t, cmp.Len(rec.created(), 0))
}

// An org ID and a token are not consent: a project that did not choose the
// circleci backend never gets a sidecar from the daemon.
func TestRunValidateNowDoesNotProvisionForAProjectThatDidNotChooseCircleCI(t *testing.T) {
	for name, content := range map[string]string{
		"no config":       "",
		"default backend": `{"orgID":"org-1","validation":{"sidecarImage":"snap-1"}}`,
		"docker backend":  `{"orgID":"org-1","factory":{"backend":"docker"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := &imageRecorder{next: fakes.NewFakeCircleCI()}
			srv := httptest.NewServer(rec)
			defer srv.Close()

			root := t.TempDir()
			if content != "" {
				writeProjectConfig(t, root, content)
			}

			d := newTestDaemon()
			d.prov = newProvisioner(newTestClient(t, srv.URL))
			ran := false
			d.runner = func(context.Context, string, string, []string, []string, io.Writer, io.Writer) int {
				ran = true
				return 0
			}

			resp := d.runValidateNow(context.Background(), smeltd.ValidateRequest{
				Args:        []string{"validate"},
				OrgID:       "org-1",
				ProjectRoot: root,
			}, nil)

			assert.Check(t, cmp.Equal(resp.ExitCode, 0))
			assert.Check(t, ran)
			assert.Check(t, cmp.Len(rec.created(), 0))
		})
	}
}

// A request that names no directory must not pick up the config of whatever
// directory the daemon happens to have been started in.
func TestRunValidateNowIgnoresTheDaemonsWorkingDirectory(t *testing.T) {
	rec := &imageRecorder{next: fakes.NewFakeCircleCI()}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	elsewhere := t.TempDir()
	writeProjectConfig(t, elsewhere, `{"factory":{"backend":"circleci"},"validation":{"sidecarImage":"snap-other-project"}}`)
	t.Chdir(elsewhere)

	d := newTestDaemon()
	d.prov = newProvisioner(newTestClient(t, srv.URL))
	d.runner = func(context.Context, string, string, []string, []string, io.Writer, io.Writer) int { return 0 }

	d.runValidateNow(context.Background(), smeltd.ValidateRequest{
		Args:  []string{"validate", "--remote"},
		OrgID: "org-1",
	}, nil)

	// No directory means no project, and no project has not chosen CircleCI.
	assert.Check(t, cmp.Len(rec.created(), 0))
}

// WorkDir wins over ProjectRoot when both hold a config.
func TestRunValidateNowPrefersWorkDirOverProjectRoot(t *testing.T) {
	rec := &imageRecorder{next: fakes.NewFakeCircleCI()}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	root := t.TempDir()
	workDir := filepath.Join(root, "sub")
	writeProjectConfig(t, root, `{"factory":{"backend":"circleci"},"validation":{"sidecarImage":"snap-root"}}`)
	writeProjectConfig(t, workDir, `{"factory":{"backend":"circleci"},"validation":{"sidecarImage":"snap-sub"}}`)

	d := newTestDaemon()
	d.prov = newProvisioner(newTestClient(t, srv.URL))
	d.runner = func(context.Context, string, string, []string, []string, io.Writer, io.Writer) int { return 0 }

	d.runValidateNow(context.Background(), smeltd.ValidateRequest{
		Args:        []string{"validate", "--remote"},
		OrgID:       "org-1",
		ProjectRoot: root,
		WorkDir:     workDir,
	}, nil)

	assert.Check(t, cmp.DeepEqual(rec.created(), []string{"snap-sub"}))
}

// The image the client resolved wins over the project config: it carries a
// per-command override the daemon cannot see, and a remote daemon may have no
// checkout to read a config from at all.
func TestRunValidateNowPrefersTheRequestedImage(t *testing.T) {
	rec := &imageRecorder{next: fakes.NewFakeCircleCI()}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	root := t.TempDir()
	writeProjectConfig(t, root, `{"factory":{"backend":"circleci"},"validation":{"sidecarImage":"snap-project"}}`)

	d := newTestDaemon()
	d.prov = newProvisioner(newTestClient(t, srv.URL))
	d.runner = func(context.Context, string, string, []string, []string, io.Writer, io.Writer) int { return 0 }

	d.runValidateNow(context.Background(), smeltd.ValidateRequest{
		Args:         []string{"validate", "test", "--remote"},
		OrgID:        "org-1",
		ProjectRoot:  root,
		SidecarImage: "snap-test",
	}, nil)

	assert.Check(t, cmp.DeepEqual(rec.created(), []string{"snap-test"}))
}

// A snapshot the API does not know is named in the error, so the user is not
// left with a bare "not found".
func TestRunValidateNowExplainsARejectedSnapshot(t *testing.T) {
	srv := httptest.NewServer(rejectImage{next: fakes.NewFakeCircleCI(), image: "snap-gone"})
	defer srv.Close()

	root := t.TempDir()
	writeProjectConfig(t, root, `{"factory":{"backend":"circleci"},"validation":{"sidecarImage":"snap-gone"}}`)

	d := newTestDaemon()
	d.prov = newProvisioner(newTestClient(t, srv.URL))
	d.runner = runnerNeverCalled(t)

	resp := d.runValidateNow(context.Background(), smeltd.ValidateRequest{
		Args:        []string{"validate", "--remote"},
		OrgID:       "org-1",
		ProjectRoot: root,
	}, nil)

	assert.Check(t, cmp.Equal(resp.ExitCode, 1))
	assert.Check(t, strings.Contains(resp.Stderr, "snap-gone"), resp.Stderr)
	assert.Check(t, strings.Contains(resp.Stderr, "validation.sidecarImage"), resp.Stderr)
}
