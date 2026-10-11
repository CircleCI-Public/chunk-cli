package sandboximage

import (
	"context"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// fakeBackend records the scripts Verify runs and fails the ones it is told to.
type fakeBackend struct {
	failing  string // substring of the script that exits 1
	scripts  []string
	created  []string
	synced   string
	deleted  []string
	repoPath string
}

func (f *fakeBackend) Create(_ context.Context, _, image, repoPath string) (sidecar.Instance, error) {
	f.created = append(f.created, image)
	f.repoPath = repoPath
	return sidecar.Instance{ID: "c1", RepoPath: repoPath}, nil
}

func (f *fakeBackend) Sync(_ context.Context, _, _, cwd string, _ bool, _ iostream.StatusFunc) error {
	f.synced = cwd
	return nil
}

func (f *fakeBackend) Exec(_ context.Context, _, script string, _ map[string]string, onOutput iostream.OutputFn, _ func(string)) (int, error) {
	f.scripts = append(f.scripts, script)
	if f.failing != "" && strings.Contains(script, f.failing) {
		onOutput(iostream.StreamStderr, []byte("boom\n"))
		return 1, nil
	}
	return 0, nil
}

func (f *fakeBackend) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func TestVerify(t *testing.T) {
	gate := &config.Command{Name: "lint", Run: "pnpm lint"}
	tests := []struct {
		name        string
		failing     string
		wantStep    string
		wantScripts int
	}{
		{"all pass", "", "", 3},
		{"kit fails first", "claude --version", StepKit, 1},
		{"install fails before gate", "pnpm install", StepInstall, 2},
		{"gate fails", "pnpm lint", StepGate, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeBackend{failing: tt.failing}
			res, err := Verify(context.Background(), f, VerifyOptions{Dir: "/src/My Repo", Image: "chunk-x:abc", Install: "pnpm install", Gate: gate})
			assert.NilError(t, err)
			assert.Equal(t, res.Step, tt.wantStep)
			assert.Equal(t, len(f.scripts), tt.wantScripts)
			assert.DeepEqual(t, f.deleted, []string{"c1"})
			assert.DeepEqual(t, f.created, []string{"chunk-x:abc"})
			assert.Equal(t, f.synced, "/src/My Repo")
			assert.Assert(t, strings.HasSuffix(f.repoPath, "/my-repo"), f.repoPath)
			if tt.wantStep != "" {
				assert.Equal(t, res.ExitCode, 1)
				assert.Equal(t, res.OutputTail, "boom")
				assert.Assert(t, !res.OK())
			}
		})
	}
}

func TestVerifySkipsMissingInstallAndGate(t *testing.T) {
	f := &fakeBackend{}
	res, err := Verify(context.Background(), f, VerifyOptions{Dir: "/src/r", Image: "i"})
	assert.NilError(t, err)
	assert.Assert(t, res.OK())
	assert.Equal(t, len(f.scripts), 1)
}

func TestPickGate(t *testing.T) {
	cmds := func(names ...string) []config.Command {
		var out []config.Command
		for _, n := range names {
			out = append(out, config.Command{Name: n, Run: n + " cmd"})
		}
		return out
	}
	tests := []struct {
		have []config.Command
		want string
	}{
		{cmds("test", "lint", "format"), "lint"},
		{cmds("test", "format"), "format"},
		{cmds("test", "typecheck"), "typecheck"},
		{cmds("install", "test"), "test"},
		{cmds("install", "build"), ""},
		{nil, ""},
	}
	for _, tt := range tests {
		got := PickGate(tt.have)
		if tt.want == "" {
			assert.Assert(t, got == nil)
			continue
		}
		assert.Equal(t, got.Name, tt.want)
	}
}
