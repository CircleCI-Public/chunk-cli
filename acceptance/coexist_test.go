package acceptance

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/testing/binary"
	testenv "github.com/smeltdev/smelt/internal/testing/env"
	"github.com/smeltdev/smelt/internal/testing/gitrepo"
)

// snapshotTree maps every file under dir to its contents, so a test can prove a
// directory was neither read-and-rewritten nor added to.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out[path] = string(data)
		return nil
	})
	assert.NilError(t, err)
	return out
}

// smelt shares a machine with CircleCI's own CLI, which keeps its state in
// .chunk/ and ~/.config/chunk. A repository that has only that setup is simply
// not set up for smelt: it gets the "run smelt init" message, and the other
// tool's files are left exactly as they were.
func TestChunkSetupIsNotReadOrTouched(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	oldProject := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(oldProject, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(oldProject, "config.json"),
		[]byte(`{"commands":[{"name":"lint","run":"echo ran-from-the-old-config"}]}`), 0o644))

	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "spec.md"), []byte("add a flag\n"), 0o644))

	env := testenv.NewTestEnv(t)
	oldUser := filepath.Join(env.HomeDir, ".config", "chunk")
	assert.NilError(t, os.MkdirAll(oldUser, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(oldUser, "config.json"),
		[]byte(`{"model":"old-model","circleciToken":"old-token"}`), 0o600))
	oldDaemon := filepath.Join(env.HomeDir, ".chunk", "daemon")
	assert.NilError(t, os.MkdirAll(oldDaemon, 0o755))

	before := map[string]map[string]string{
		"project": snapshotTree(t, oldProject),
		"user":    snapshotTree(t, oldUser),
		"home":    snapshotTree(t, filepath.Join(env.HomeDir, ".chunk")),
	}

	for _, args := range [][]string{
		{"validate", "--no-daemon"},
		{"factory", "build", "--no-daemon", "--no-publish", "--plan", "spec.md"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			result := binary.RunCLI(t, args, env, workDir)
			out := result.Stdout + result.Stderr
			assert.Assert(t, result.ExitCode != 0, out)
			assert.Assert(t, strings.Contains(out, "smelt init"), out)
			assert.Assert(t, !strings.Contains(out, "ran-from-the-old-config"), out)
		})
	}

	assert.DeepEqual(t, snapshotTree(t, oldProject), before["project"])
	assert.DeepEqual(t, snapshotTree(t, oldUser), before["user"])
	assert.DeepEqual(t, snapshotTree(t, filepath.Join(env.HomeDir, ".chunk")), before["home"])
}
