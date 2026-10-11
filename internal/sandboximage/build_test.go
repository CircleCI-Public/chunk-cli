package sandboximage

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/envspec"
)

func TestReadBuildStream(t *testing.T) {
	stream := `{"stream":"Step 1/3 : FROM x\n"}
{"stream":" ---> abc\n"}
{"aux":{"ID":"sha256:1"}}
{"errorDetail":{"message":"boom"},"error":"The command '/bin/sh -c false' returned a non-zero code: 1"}
`
	var lines []string
	err := readBuildStream(strings.NewReader(stream), func(l string) { lines = append(lines, l) })
	assert.ErrorContains(t, err, "non-zero code")
	assert.DeepEqual(t, lines, []string{"Step 1/3 : FROM x", " ---> abc", "The command '/bin/sh -c false' returned a non-zero code: 1"})

	lines = nil
	assert.NilError(t, readBuildStream(strings.NewReader(`{"stream":"ok\n"}`+"\n"), func(l string) { lines = append(lines, l) }))
	assert.DeepEqual(t, lines, []string{"ok"})
}

func TestTailKeepsLastLines(t *testing.T) {
	var tl tail
	for i := range logTailLines + 10 {
		tl.add(strings.Repeat("x", i%3+1))
	}
	assert.Equal(t, len(tl.lines), logTailLines)
}

// fakeDocker is a daemon that has some images and answers builds with a canned stream.
type fakeDocker struct {
	images  map[string]bool
	stream  string
	builds  []build.ImageBuildOptions
	context []byte
}

func (f *fakeDocker) ImageBuild(_ context.Context, r io.Reader, o build.ImageBuildOptions) (build.ImageBuildResponse, error) {
	f.builds = append(f.builds, o)
	f.context, _ = io.ReadAll(r)
	return build.ImageBuildResponse{Body: io.NopCloser(strings.NewReader(f.stream))}, nil
}

func (f *fakeDocker) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (image.InspectResponse, error) {
	if f.images[ref] {
		return image.InspectResponse{}, nil
	}
	return image.InspectResponse{}, errdefs.ErrNotFound
}

func (f *fakeDocker) ImagePull(context.Context, string, image.PullOptions) (io.ReadCloser, error) {
	return nil, errors.New("no network")
}

func goPlan(t *testing.T) Plan {
	t.Helper()
	dir := writeRepo(t, map[string]string{
		"Dockerfile.smelt": "FROM x\nCOPY go.mod /tmp/\n",
		"go.mod":           "module a\n",
	})
	p, err := NewPlan(dir, "Dockerfile.smelt")
	assert.NilError(t, err)
	return p
}

func TestEnsureSkipsAnExistingTag(t *testing.T) {
	plan := goPlan(t)
	f := &fakeDocker{images: map[string]bool{plan.Tag: true}}
	built, err := NewBuilder(f).Ensure(context.Background(), plan, EnsureOptions{})
	assert.NilError(t, err)
	assert.Assert(t, !built)
	assert.Equal(t, len(f.builds), 0)
}

func TestEnsureBuildsAMissingTagWithLabelAndLog(t *testing.T) {
	plan := goPlan(t)
	f := &fakeDocker{stream: `{"stream":"Step 1/2 : FROM x\n"}` + "\n"}
	logPath := filepath.Join(t.TempDir(), "logs", "build.log")
	var told string
	built, err := NewBuilder(f).Ensure(context.Background(), plan, EnsureOptions{LogPath: logPath, Building: func(tag string) { told = tag }})
	assert.NilError(t, err)
	assert.Assert(t, built)
	assert.Equal(t, told, plan.Tag)
	assert.DeepEqual(t, f.builds[0].Tags, []string{plan.Tag})
	assert.Equal(t, f.builds[0].Labels[LabelSandbox], plan.Slug)
	assert.Equal(t, f.builds[0].Dockerfile, "Dockerfile.smelt")
	assert.Assert(t, !f.builds[0].NoCache)
	logged, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(logged), "Step 1/2"))
	assert.Assert(t, strings.Contains(string(f.context), "module a"))
}

func TestEnsureRebuildIgnoresTheCache(t *testing.T) {
	plan := goPlan(t)
	f := &fakeDocker{images: map[string]bool{plan.Tag: true}}
	built, err := NewBuilder(f).Ensure(context.Background(), plan, EnsureOptions{Rebuild: true})
	assert.NilError(t, err)
	assert.Assert(t, built)
	assert.Assert(t, f.builds[0].NoCache)
}

func TestBuildFailureCarriesTheLogTail(t *testing.T) {
	plan := goPlan(t)
	f := &fakeDocker{stream: `{"stream":"E: Unable to locate package nodjs\n"}` + "\n" + `{"error":"returned a non-zero code: 100"}` + "\n"}
	_, err := NewBuilder(f).Ensure(context.Background(), plan, EnsureOptions{})
	var be *BuildError
	assert.Assert(t, errors.As(err, &be), "%v", err)
	assert.Equal(t, be.Tag, plan.Tag)
	assert.Assert(t, strings.Contains(be.LogTail, "nodjs"))
}

// TestBuildLive builds a tiny image on a real daemon; it skips without one.
func TestBuildLive(t *testing.T) {
	b, err := Connect()
	if err != nil {
		t.Skipf("no docker: %v", err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.Exists(context.Background(), "smelt-live-probe:none"); err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}
	dir := writeRepo(t, map[string]string{
		"Dockerfile.smelt": "FROM busybox:stable\nCOPY go.mod /tmp/go.mod\nCMD [\"sleep\", \"infinity\"]\n",
		"go.mod":           "module tiny\n",
	})
	plan, err := NewPlan(dir, "Dockerfile.smelt")
	assert.NilError(t, err)
	_, err = b.Ensure(context.Background(), plan, EnsureOptions{})
	assert.NilError(t, err)
	exists, err := b.Exists(context.Background(), plan.Tag)
	assert.NilError(t, err)
	assert.Assert(t, exists)
}

// TestKitLive builds the agent kit on a real daemon and finds both agent CLIs
// in the image. It skips without Docker, and with -short: it downloads both.
func TestKitLive(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads both agent CLIs")
	}
	b, err := Connect()
	if err != nil {
		t.Skipf("no docker: %v", err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.Exists(context.Background(), "smelt-live-probe:none"); err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("no docker CLI to run the image with")
	}
	dir := writeRepo(t, map[string]string{
		"Dockerfile.smelt": Render(NewRecipe(&envspec.Environment{Stack: "unknown"}, nil, nil)),
	})
	plan, err := NewPlan(dir, "Dockerfile.smelt")
	assert.NilError(t, err)
	_, err = b.Ensure(context.Background(), plan, EnsureOptions{})
	assert.NilError(t, err)
	out, err := exec.Command(docker, "run", "--rm", plan.Tag, "sh", "-c", "codex --version && claude --version").CombinedOutput()
	assert.NilError(t, err, string(out))
	assert.Assert(t, strings.Contains(string(out), "codex"), string(out))
}
