package sandboximage

import (
	"context"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/codex"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/sidecar"
	"github.com/smeltdev/smelt/internal/testing/gitrepo"
)

// fakeBackend records the scripts Verify runs and fails the ones it is told to.
type fakeBackend struct {
	failing  string // substring of the script that exits 1
	output   string // what the failing script prints, "boom" when empty
	env      map[string]string
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

func (f *fakeBackend) Exec(_ context.Context, _, script string, env map[string]string, onOutput iostream.OutputFn, _ func(string)) (int, error) {
	f.scripts = append(f.scripts, script)
	if f.failing != "" && strings.Contains(script, f.failing) {
		f.env = env
		out := f.output
		if out == "" {
			out = "boom"
		}
		onOutput(iostream.StreamStderr, []byte(out+"\n"))
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
	// The workspace is named after the origin remote's repo, as a real run
	// names it, not after the directory.
	dir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	tests := []struct {
		name        string
		failing     string
		wantStep    string
		wantScripts int
	}{
		{"all pass", "", "", 3},
		{"kit fails first", "claude --version", StepKit, 1},
		{"kit fails without the codex command host", `test -x "$HOME/.local/bin/codex-code-mode-host"`, StepKit, 1},
		{"install fails before gate", "pnpm install", StepInstall, 2},
		{"gate fails", "pnpm lint", StepGate, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeBackend{failing: tt.failing}
			res, err := Verify(context.Background(), f, VerifyOptions{Dir: dir, Image: "smelt-x:abc", Install: "pnpm install", Gate: gate})
			assert.NilError(t, err)
			assert.Equal(t, res.Step, tt.wantStep)
			assert.Equal(t, len(f.scripts), tt.wantScripts)
			assert.DeepEqual(t, f.deleted, []string{"c1"})
			assert.DeepEqual(t, f.created, []string{"smelt-x:abc"})
			assert.Equal(t, f.synced, dir)
			assert.Equal(t, f.repoPath, sidecar.DefaultWorkspace("my-repo"))
			assert.Assert(t, strings.HasPrefix(f.scripts[0], "cd "+sidecar.ShellEscape(f.repoPath)+" && "), f.scripts[0])
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
	res, err := Verify(context.Background(), f, VerifyOptions{Dir: gitrepo.SetupGitRepo(t, "my-org", "r"), Image: "i"})
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

func TestKitCheckTestsTheCodexCommandHost(t *testing.T) {
	assert.Assert(t, strings.Contains(kitCheck, `test -x "$HOME/.local/bin/codex-code-mode-host"`))
}

func TestVerifyCodexTurn(t *testing.T) {
	dir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	smoke := &CodexSmoke{Harness: codex.Harness{}, Credential: harness.Credential{EnvVar: codex.EnvAPIKey, Value: "sk-test"}}
	tests := []struct {
		name       string
		smoke      *CodexSmoke
		output     string
		failing    string
		wantStep   string
		wantScript int
		wantNote   string
	}{
		{name: "no login: binary check only", wantScript: 1, wantNote: "binary check only"},
		{name: "turn passes", smoke: smoke, wantScript: 2, wantNote: "codex turn ✓"},
		{name: "turn fails", smoke: smoke, failing: "codex exec", wantStep: StepKit, wantScript: 2},
		{name: "host missing is a failure", smoke: smoke, failing: "codex exec", output: "codex-code-mode-host is missing", wantStep: StepKit, wantScript: 2},
		{name: "usage limit is not a failure", smoke: smoke, failing: "codex exec", output: "You've hit your usage limit. Try again later.", wantScript: 2, wantNote: "could not confirm"},
		{name: "401 is not a failure", smoke: smoke, failing: "codex exec", output: "unexpected status 401 Unauthorized", wantScript: 2, wantNote: "could not confirm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeBackend{failing: tt.failing, output: tt.output}
			var notes []string
			res, err := Verify(context.Background(), f, VerifyOptions{
				Dir: dir, Image: "i", Codex: tt.smoke,
				Progress: func(_ iostream.Level, msg string) { notes = append(notes, msg) },
			})
			assert.NilError(t, err)
			assert.Equal(t, res.Step, tt.wantStep)
			assert.Equal(t, len(f.scripts), tt.wantScript)
			assert.Assert(t, strings.Contains(strings.Join(notes, "\n"), tt.wantNote), notes)
			if tt.smoke != nil {
				last := f.scripts[len(f.scripts)-1]
				assert.Assert(t, strings.Contains(last, `timeout 60 codex exec --skip-git-repo-check "reply ok"`), last)
			}
		})
	}
}

func TestVerifyCodexTurnGetsTheCredential(t *testing.T) {
	f := &fakeBackend{failing: "codex exec"}
	smoke := &CodexSmoke{Harness: codex.Harness{}, Credential: harness.Credential{EnvVar: codex.EnvAPIKey, Value: "sk-test"}}
	_, err := Verify(context.Background(), f, VerifyOptions{Dir: gitrepo.SetupGitRepo(t, "my-org", "r"), Image: "i", Codex: smoke})
	assert.NilError(t, err)
	assert.Equal(t, f.env[codex.EnvAPIKey], "sk-test")
}
