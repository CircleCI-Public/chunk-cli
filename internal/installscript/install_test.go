// Package installscript tests install.sh end to end against a fake release
// server: the script is run as users run it, with bash, and nothing else is
// stubbed.
package installscript

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// release is one fake GitHub release.
type release struct {
	archive   []byte
	checksums string
}

// archiveName is the archive install.sh asks for on this machine.
func archiveName(t *testing.T) string {
	t.Helper()
	var osName, arch string
	switch runtime.GOOS {
	case "linux":
		osName = "Linux"
	case "darwin":
		osName = "Darwin"
	default:
		t.Skipf("install.sh supports linux and darwin, not %s", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "arm64"
	default:
		t.Skipf("install.sh supports amd64 and arm64, not %s", runtime.GOARCH)
	}
	return fmt.Sprintf("smelt_%s_%s.tar.gz", osName, arch)
}

// fakeSmelt is a release's binary: it prints its own version.
func fakeSmelt(version string) []byte {
	return []byte("#!/bin/sh\necho \"smelt version " + version + "\"\n")
}

func tarball(t *testing.T, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	assert.NilError(t, tw.WriteHeader(&tar.Header{Name: "smelt", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}))
	_, err := tw.Write(binary)
	assert.NilError(t, err)
	assert.NilError(t, tw.Close())
	assert.NilError(t, gz.Close())
	return buf.Bytes()
}

func goodRelease(t *testing.T, version string) release {
	t.Helper()
	archive := tarball(t, fakeSmelt(version))
	sum := sha256.Sum256(archive)
	return release{
		archive:   archive,
		checksums: hex.EncodeToString(sum[:]) + "  " + archiveName(t) + "\n" + strings.Repeat("0", 64) + "  other.tar.gz\n",
	}
}

// fakeReleases serves releases (keyed by version, no "v") the way GitHub does:
// /releases/latest redirects to the tag page of `latest`, and downloads live
// under /releases/download/v<version>/.
func fakeReleases(t *testing.T, latest string, releases map[string]release) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if latest == "" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/releases/tag/v"+latest, http.StatusFound)
	})
	mux.HandleFunc("/releases/tag/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("release page"))
	})
	mux.HandleFunc("/releases/download/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/releases/download/v")
		version, file, _ := strings.Cut(rest, "/")
		rel, ok := releases[version]
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch file {
		case archiveName(t):
			_, _ = w.Write(rel.archive)
		case "checksums.txt":
			_, _ = w.Write([]byte(rel.checksums))
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func requireTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "tar", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	_, errSum := exec.LookPath("sha256sum")
	_, errShasum := exec.LookPath("shasum")
	if errSum != nil && errShasum != nil {
		t.Skip("no sha256sum or shasum available")
	}
}

type runOptions struct {
	base    string
	destDir string // "" leaves DESTDIR unset
	version string
	force   bool
	shell   string
	pathHas string // directory added to PATH
	home    string
}

type result struct {
	output string
	err    error
}

// runInstall runs install.sh with a clean environment.
func runInstall(t *testing.T, o runOptions) result {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "install.sh"))
	assert.NilError(t, err)

	path := os.Getenv("PATH")
	if o.pathHas != "" {
		path = o.pathHas + string(os.PathListSeparator) + path
	}
	env := []string{
		"PATH=" + path,
		"HOME=" + o.home,
		"TMPDIR=" + t.TempDir(),
		"NO_COLOR=1",
		"SHELL=" + o.shell,
		"SMELT_DOWNLOAD_BASE=" + o.base,
		"no_proxy=*",
		"NO_PROXY=*",
	}
	if o.destDir != "" {
		env = append(env, "DESTDIR="+o.destDir)
	}
	if o.version != "" {
		env = append(env, "VERSION="+o.version)
	}
	if o.force {
		env = append(env, "SMELT_FORCE=1")
	}
	cmd := exec.Command("bash", script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return result{output: string(out), err: err}
}

func smeltVersion(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command(path).CombinedOutput()
	assert.NilError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

func setup(t *testing.T) (home, dest string) {
	t.Helper()
	requireTools(t)
	home = t.TempDir()
	return home, filepath.Join(home, "bin")
}

func TestInstallLatest(t *testing.T) {
	home, dest := setup(t)
	srv := fakeReleases(t, "1.2.0", map[string]release{"1.2.0": goodRelease(t, "1.2.0")})

	res := runInstall(t, runOptions{base: srv.URL, destDir: dest, home: home, shell: "/bin/zsh"})
	assert.NilError(t, res.err, res.output)

	assert.Equal(t, smeltVersion(t, filepath.Join(dest, "smelt")), "smelt version 1.2.0")
	assert.Assert(t, strings.Contains(res.output, "cd your-repo && smelt init"), res.output)
}

func TestInstallDefaultsToHomeLocalBin(t *testing.T) {
	home, _ := setup(t)
	srv := fakeReleases(t, "1.2.0", map[string]release{"1.2.0": goodRelease(t, "1.2.0")})

	res := runInstall(t, runOptions{base: srv.URL, home: home, shell: "/bin/bash"})
	assert.NilError(t, res.err, res.output)

	// Created on demand, with no sudo.
	assert.Equal(t, smeltVersion(t, filepath.Join(home, ".local", "bin", "smelt")), "smelt version 1.2.0")
	assert.Assert(t, !strings.Contains(res.output, "sudo"), res.output)
}

func TestInstallPinnedVersionWithOrWithoutV(t *testing.T) {
	for _, pin := range []string{"1.0.0", "v1.0.0"} {
		t.Run(pin, func(t *testing.T) {
			home, dest := setup(t)
			srv := fakeReleases(t, "1.2.0", map[string]release{
				"1.0.0": goodRelease(t, "1.0.0"),
				"1.2.0": goodRelease(t, "1.2.0"),
			})

			res := runInstall(t, runOptions{base: srv.URL, destDir: dest, home: home, version: pin, shell: "/bin/zsh"})
			assert.NilError(t, res.err, res.output)
			assert.Equal(t, smeltVersion(t, filepath.Join(dest, "smelt")), "smelt version 1.0.0")
		})
	}
}

func TestInstallChecksumMismatchInstallsNothing(t *testing.T) {
	home, dest := setup(t)
	rel := goodRelease(t, "1.2.0")
	rel.archive = tarball(t, fakeSmelt("tampered")) // no longer what checksums.txt vouches for
	srv := fakeReleases(t, "1.2.0", map[string]release{"1.2.0": rel})

	res := runInstall(t, runOptions{base: srv.URL, destDir: dest, home: home, shell: "/bin/zsh"})
	assert.Assert(t, res.err != nil, res.output)
	assert.Assert(t, strings.Contains(res.output, "Checksum mismatch"), res.output)
	assertNothingInstalled(t, dest)
}

func TestInstallMissingChecksumLineIsRefused(t *testing.T) {
	home, dest := setup(t)
	rel := goodRelease(t, "1.2.0")
	rel.checksums = strings.Repeat("a", 64) + "  some-other-archive.tar.gz\n"
	srv := fakeReleases(t, "1.2.0", map[string]release{"1.2.0": rel})

	res := runInstall(t, runOptions{base: srv.URL, destDir: dest, home: home, shell: "/bin/zsh"})
	assert.Assert(t, res.err != nil, res.output)
	assert.Assert(t, strings.Contains(res.output, "No checksum entry"), res.output)
	assertNothingInstalled(t, dest)
}

func TestInstallUnknownVersionFailsCleanly(t *testing.T) {
	home, dest := setup(t)
	srv := fakeReleases(t, "1.2.0", map[string]release{"1.2.0": goodRelease(t, "1.2.0")})

	res := runInstall(t, runOptions{base: srv.URL, destDir: dest, home: home, version: "9.9.9", shell: "/bin/zsh"})
	assert.Assert(t, res.err != nil, res.output)
	assert.Assert(t, strings.Contains(res.output, "Download failed"), res.output)
	assertNothingInstalled(t, dest)
}

func TestInstallWithNoReleasesFailsCleanly(t *testing.T) {
	home, dest := setup(t)
	srv := fakeReleases(t, "", nil)

	res := runInstall(t, runOptions{base: srv.URL, destDir: dest, home: home, shell: "/bin/zsh"})
	assert.Assert(t, res.err != nil, res.output)
	assert.Assert(t, strings.Contains(res.output, "latest version"), res.output)
	assertNothingInstalled(t, dest)
}

func TestInstallRerunUpgradesInPlace(t *testing.T) {
	home, dest := setup(t)
	releases := map[string]release{"1.0.0": goodRelease(t, "1.0.0"), "1.1.0": goodRelease(t, "1.1.0")}

	srv := fakeReleases(t, "1.0.0", releases)
	res := runInstall(t, runOptions{base: srv.URL, destDir: dest, home: home, shell: "/bin/zsh"})
	assert.NilError(t, res.err, res.output)
	assert.Equal(t, smeltVersion(t, filepath.Join(dest, "smelt")), "smelt version 1.0.0")

	srv2 := fakeReleases(t, "1.1.0", releases)
	res = runInstall(t, runOptions{base: srv2.URL, destDir: dest, home: home, shell: "/bin/zsh"})
	assert.NilError(t, res.err, res.output)
	assert.Equal(t, smeltVersion(t, filepath.Join(dest, "smelt")), "smelt version 1.1.0")

	// The staging file is renamed over the binary, never left behind.
	entries, err := os.ReadDir(dest)
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 1, "unexpected files in %s: %v", dest, entries)
}

func TestInstallRefusesToReplaceASymlink(t *testing.T) {
	home, dest := setup(t)
	srv := fakeReleases(t, "1.2.0", map[string]release{"1.2.0": goodRelease(t, "1.2.0")})

	// What `task dev-install` leaves: bin/smelt -> a working-tree build.
	devBuild := filepath.Join(home, "dist-smelt")
	assert.NilError(t, os.WriteFile(devBuild, fakeSmelt("dev-build"), 0o755))
	assert.NilError(t, os.MkdirAll(dest, 0o755))
	assert.NilError(t, os.Symlink(devBuild, filepath.Join(dest, "smelt")))

	res := runInstall(t, runOptions{base: srv.URL, destDir: dest, home: home, shell: "/bin/zsh"})
	assert.Assert(t, res.err != nil, res.output)
	assert.Assert(t, strings.Contains(res.output, "symlink"), res.output)
	assert.Assert(t, strings.Contains(res.output, "SMELT_FORCE=1"), res.output)
	target, err := os.Readlink(filepath.Join(dest, "smelt"))
	assert.NilError(t, err)
	assert.Equal(t, target, devBuild)
	assert.Equal(t, smeltVersion(t, devBuild), "smelt version dev-build")

	t.Run("SMELT_FORCE=1 replaces it", func(t *testing.T) {
		res := runInstall(t, runOptions{base: srv.URL, destDir: dest, home: home, shell: "/bin/zsh", force: true})
		assert.NilError(t, res.err, res.output)

		info, err := os.Lstat(filepath.Join(dest, "smelt"))
		assert.NilError(t, err)
		assert.Assert(t, info.Mode()&os.ModeSymlink == 0, "still a symlink")
		assert.Equal(t, smeltVersion(t, filepath.Join(dest, "smelt")), "smelt version 1.2.0")
		// The dev build the symlink pointed at is untouched.
		assert.Equal(t, smeltVersion(t, devBuild), "smelt version dev-build")
	})
}

func TestInstallPathHint(t *testing.T) {
	srv := func(t *testing.T) *httptest.Server {
		return fakeReleases(t, "1.2.0", map[string]release{"1.2.0": goodRelease(t, "1.2.0")})
	}

	t.Run("shown for the user's shell when DESTDIR is not on PATH", func(t *testing.T) {
		for shell, want := range map[string]string{
			"/bin/zsh":               ">> ~/.zshrc",
			"/usr/bin/bash":          `export PATH="`,
			"/opt/homebrew/bin/fish": "fish_add_path ",
		} {
			t.Run(filepath.Base(shell), func(t *testing.T) {
				home, dest := setup(t)
				res := runInstall(t, runOptions{base: srv(t).URL, destDir: dest, home: home, shell: shell})
				assert.NilError(t, res.err, res.output)
				assert.Assert(t, strings.Contains(res.output, "is not on your PATH"), res.output)
				assert.Assert(t, strings.Contains(res.output, want), "missing %q in:\n%s", want, res.output)
				assert.Assert(t, strings.Contains(res.output, dest), res.output)
			})
		}
	})

	t.Run("spelled with $HOME for the default directory", func(t *testing.T) {
		home, _ := setup(t)
		res := runInstall(t, runOptions{base: srv(t).URL, home: home, shell: "/bin/zsh"})
		assert.NilError(t, res.err, res.output)
		assert.Assert(t, strings.Contains(res.output, `export PATH="$HOME/.local/bin:$PATH"`), res.output)
	})

	t.Run("silent when DESTDIR is already on PATH", func(t *testing.T) {
		home, dest := setup(t)
		res := runInstall(t, runOptions{base: srv(t).URL, destDir: dest, home: home, shell: "/bin/zsh", pathHas: dest})
		assert.NilError(t, res.err, res.output)
		assert.Assert(t, !strings.Contains(res.output, "is not on your PATH"), res.output)
	})
}

func assertNothingInstalled(t *testing.T, dest string) {
	t.Helper()
	entries, err := os.ReadDir(dest)
	if os.IsNotExist(err) {
		return
	}
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 0, "files left in %s: %v", dest, entries)
}
