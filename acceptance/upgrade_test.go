package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/upgrade"
)

func makeFakeArchive(t *testing.T) []byte {
	t.Helper()
	content := []byte("#!/bin/sh\necho chunk updated\n")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "chunk", Mode: 0o755, Size: int64(len(content))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// newUpgradeFakeServer serves a release of the fork: the archive, and the
// checksums.txt that vouches for it (checksums, when non-empty, replaces the
// correct body).
func newUpgradeFakeServer(t *testing.T, archive []byte, checksums string) *httptest.Server {
	t.Helper()
	if checksums == "" {
		sum := sha256.Sum256(archive)
		checksums = hex.EncodeToString(sum[:]) + "  " + upgrade.PlatformAssetName() + "\n"
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/"+upgrade.Repo+"/releases/latest":
			assetName := upgrade.PlatformAssetName()
			release := map[string]any{
				"tag_name": "v9.9.9",
				"assets": []map[string]any{
					{"name": assetName, "browser_download_url": srv.URL + "/download/" + assetName},
					{"name": upgrade.ChecksumsAsset, "browser_download_url": srv.URL + "/download/" + upgrade.ChecksumsAsset},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(release)
		case r.URL.Path == "/download/"+upgrade.ChecksumsAsset:
			w.Write([]byte(checksums))
		case strings.HasPrefix(r.URL.Path, "/download/"):
			w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// upgradeEnv is an environment whose binary looks like a release, since a dev
// build refuses to upgrade.
func upgradeEnv(t *testing.T, srv *httptest.Server, installPath string) *testenv.TestEnv {
	t.Helper()
	env := testenv.NewTestEnv(t)
	env.Binary = binary.BuildVersioned(t, "1.0.0")
	env.GithubURL = srv.URL
	env.Extra["CHUNK_INSTALL_PATH"] = installPath
	return env
}

func TestUpgradeHappyPath(t *testing.T) {
	archive := makeFakeArchive(t)
	srv := newUpgradeFakeServer(t, archive, "")

	// Redirect the binary replacement to a temp file so we don't overwrite the test binary.
	installPath := filepath.Join(t.TempDir(), "chunk")
	if err := os.WriteFile(installPath, []byte("placeholder"), 0o755); err != nil {
		t.Fatal(err)
	}

	env := upgradeEnv(t, srv, installPath)
	result := binary.RunCLI(t, []string{"upgrade"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0,
		"expected successful upgrade, stdout: %s, stderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "v9.9.9"),
		"expected version in output, got: %s", combined)

	// Verify the binary was actually replaced.
	got, err := os.ReadFile(installPath)
	assert.NilError(t, err)
	assert.Assert(t, !bytes.Equal(got, []byte("placeholder")),
		"expected install path to be overwritten")
}

func TestUpgradeRejectsATamperedArchive(t *testing.T) {
	srv := newUpgradeFakeServer(t, makeFakeArchive(t), strings.Repeat("0", 64)+"  "+upgrade.PlatformAssetName()+"\n")

	installPath := filepath.Join(t.TempDir(), "chunk")
	assert.NilError(t, os.WriteFile(installPath, []byte("placeholder"), 0o755))

	env := upgradeEnv(t, srv, installPath)
	result := binary.RunCLI(t, []string{"upgrade"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected a checksum failure, stdout: %s, stderr: %s", result.Stdout, result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout+result.Stderr, "checksum mismatch"), result.Stdout+result.Stderr)
	got, err := os.ReadFile(installPath)
	assert.NilError(t, err)
	assert.Equal(t, string(got), "placeholder")
}

func TestUpgradeRefusesASourceBuild(t *testing.T) {
	srv := newUpgradeFakeServer(t, makeFakeArchive(t), "")

	installPath := filepath.Join(t.TempDir(), "chunk")
	assert.NilError(t, os.WriteFile(installPath, []byte("placeholder"), 0o755))

	// The default test binary is a dev build.
	env := testenv.NewTestEnv(t)
	env.GithubURL = srv.URL
	env.Extra["CHUNK_INSTALL_PATH"] = installPath
	result := binary.RunCLI(t, []string{"upgrade"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "stdout: %s, stderr: %s", result.Stdout, result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout+result.Stderr, "git pull && task build"), result.Stdout+result.Stderr)
	got, err := os.ReadFile(installPath)
	assert.NilError(t, err)
	assert.Equal(t, string(got), "placeholder")
}

func TestUpgradeBrewManaged(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_INSTALL_PATH"] = "/opt/homebrew/bin/chunk"

	result := binary.RunCLI(t, []string{"upgrade"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for brew-managed install")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Homebrew"),
		"expected error mentioning Homebrew, got: %s", combined)
}

func TestUpgradeAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	installPath := filepath.Join(t.TempDir(), "chunk")
	assert.NilError(t, os.WriteFile(installPath, []byte("placeholder"), 0o755))
	env := upgradeEnv(t, srv, installPath)

	result := binary.RunCLI(t, []string{"upgrade"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit on API error")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "fetch latest release"),
		"expected error about fetching release, got: %s", combined)
}
