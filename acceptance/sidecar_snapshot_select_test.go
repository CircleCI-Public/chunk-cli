package acceptance

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// writeRemoteCommandConfig writes a .chunk/config.json with one remote command
// and no validation.sidecarImage — the "repo with no sidecar image" case.
func writeRemoteCommandConfig(t *testing.T, workDir string) {
	t.Helper()
	chunkDir := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	cfg := map[string]any{
		"commands": []map[string]any{
			{"name": "test", "run": "echo test-output", "remote": true},
		},
	}
	data, err := json.Marshal(cfg)
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), data, 0o644))
}

// createdSidecarImage returns the image attribute of the single create-sidecar
// request recorded by cci.
func createdSidecarImage(t *testing.T, cci *fakes.FakeCircleCI) any {
	t.Helper()
	createReqs := filterByPath(cci.Recorder.AllRequests(), "/api/v3/sidecar/instances")
	assert.Equal(t, len(createReqs), 1, "expected exactly 1 create-sidecar request")

	var body map[string]any
	assert.NilError(t, json.Unmarshal(createReqs[0].Body, &body))
	envelope, ok := body["data"].(map[string]any)
	assert.Assert(t, ok, "expected data envelope in request body")
	attrs, ok := envelope["attributes"].(map[string]any)
	assert.Assert(t, ok, "expected attributes in data envelope")
	return attrs["image"]
}

// runValidateForSnapshotSelection runs `chunk validate` in a fresh repo with no
// configured sidecar image, against the given org snapshots. seedFiles are
// written into the working tree first so stack detection has something to read.
// Sync fails because no SSH server is listening, which is fine: sidecar
// creation happens first and is all these tests assert on.
func runValidateForSnapshotSelection(t *testing.T, snapshots []fakes.Snapshot, seedFiles map[string]string) *fakes.FakeCircleCI {
	t.Helper()
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = "127.0.0.1"
	cci.Snapshots = snapshots
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	for name, content := range seedFiles {
		assert.NilError(t, os.WriteFile(filepath.Join(workDir, name), []byte(content), 0o644))
	}
	writeRemoteCommandConfig(t, workDir)

	sshDir := filepath.Join(t.TempDir(), ".ssh")
	assert.NilError(t, os.MkdirAll(sshDir, 0o700))
	identityFile := filepath.Join(sshDir, "chunk_ai")
	assert.NilError(t, generateTestSSHKey(t, identityFile))

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	env.Extra["CIRCLECI_ORG_ID"] = "org-aaa"

	binary.RunCLI(t, []string{"validate", "--identity-file", identityFile}, env, workDir)
	return cci
}

// A repo with no validation.sidecarImage should still land on the org snapshot
// built for it, rather than on the bare default image.
func TestValidateSelectsSnapshotMatchingRepo(t *testing.T) {
	cci := runValidateForSnapshotSelection(t, []fakes.Snapshot{
		{ID: "snap-unrelated", OrgID: "org-aaa", Name: "billing-service"},
		{ID: "snap-repo", OrgID: "org-aaa", Name: "test-repo"},
	}, nil)
	assert.Equal(t, createdSidecarImage(t, cci), "snap-repo")
}

// With no repo-specific snapshot, the stack detected from the working tree is
// the next best signal.
func TestValidateFallsBackToStackSnapshot(t *testing.T) {
	cci := runValidateForSnapshotSelection(t, []fakes.Snapshot{
		{ID: "snap-python", OrgID: "org-aaa", Name: "python-base"},
		{ID: "snap-go", OrgID: "org-aaa", Name: "go-base"},
	}, map[string]string{"go.mod": "module example.com/test-repo\n"})
	assert.Equal(t, createdSidecarImage(t, cci), "snap-go")
}

// Nothing related to the repo means no image: guessing wrong is worse than
// falling back to the default, which is what the CLI did before selection.
func TestValidateKeepsDefaultImageWhenNoSnapshotMatches(t *testing.T) {
	cci := runValidateForSnapshotSelection(t, []fakes.Snapshot{
		{ID: "snap-unrelated", OrgID: "org-aaa", Name: "billing-service"},
	}, nil)
	image := createdSidecarImage(t, cci)
	assert.Assert(t, image == nil || image == "", "expected no image, got %v", image)
}

// A snapshot in another org must never be selected.
func TestValidateIgnoresSnapshotsFromOtherOrgs(t *testing.T) {
	cci := runValidateForSnapshotSelection(t, []fakes.Snapshot{
		{ID: "snap-other-org", OrgID: "org-bbb", Name: "test-repo"},
	}, nil)
	image := createdSidecarImage(t, cci)
	assert.Assert(t, image == nil || image == "", "expected no image, got %v", image)
}
