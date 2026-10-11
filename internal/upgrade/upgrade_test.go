package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
)

// makeTarGz returns a .tar.gz laid out like a release archive, with the binary
// last. It deliberately includes a nested file whose base name is also "smelt":
// packaging no longer ships one, but the reader must not depend on that, so
// this pins it against regressing to base-name matching.
func makeTarGz(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	entries := []struct {
		name string
		body []byte
	}{
		{"LICENSE", []byte("MIT\n")},
		{"README.md", []byte("# smelt\n")},
		{"share/bash-completion/completions/smelt", []byte("# bash completion for smelt\n")},
		{"share/zsh/site-functions/_smelt", []byte("#compdef smelt\n")},
		{"docs/smelt", []byte("not the binary either\n")},
		{"smelt", content},
	}
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: tar.TypeReg,
			Mode:     0o755,
			Size:     int64(len(e.body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// releaseFixture controls what the fake release serves.
type releaseFixture struct {
	apiStatus int
	archive   []byte // nil: the download 404s
	// checksums is the body of checksums.txt; "" serves the correct one for
	// archive, and noChecksums leaves the asset out of the release entirely.
	checksums   string
	noChecksums bool
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// fakeServer sets up an httptest server that serves a GitHub-like release API
// and a download endpoint. Returns the server and the install target path.
func fakeServer(t *testing.T, fx releaseFixture) (*httptest.Server, string) {
	t.Helper()

	installDir := t.TempDir()
	installPath := filepath.Join(installDir, "smelt")

	checksums := fx.checksums
	if checksums == "" && fx.archive != nil {
		checksums = sha(fx.archive) + "  " + PlatformAssetName() + "\n"
	}

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/"+Repo+"/releases/latest" {
			if fx.apiStatus != http.StatusOK {
				w.WriteHeader(fx.apiStatus)
				return
			}
			rel := ghRelease{
				TagName: "v9.9.9",
				Assets: []ghAsset{
					{Name: PlatformAssetName(), BrowserDownloadURL: srv.URL + "/download/" + PlatformAssetName()},
				},
			}
			if !fx.noChecksums {
				rel.Assets = append(rel.Assets, ghAsset{Name: ChecksumsAsset, BrowserDownloadURL: srv.URL + "/download/" + ChecksumsAsset})
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rel)
			return
		}
		switch r.URL.Path {
		case "/download/" + ChecksumsAsset:
			w.Write([]byte(checksums))
		case "/download/" + PlatformAssetName():
			if fx.archive == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(fx.archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return srv, installPath
}

func runOpts(srv *httptest.Server, installPath string) Options {
	return Options{Out: io.Discard, Client: srv.Client(), APIBase: srv.URL, InstallPath: installPath, Version: "v1.0.0"}
}

func TestRun(t *testing.T) {
	fakeBinary := []byte("#!/bin/sh\necho smelt v9.9.9\n")
	archiveData := makeTarGz(t, fakeBinary)

	tests := []struct {
		name        string
		fx          releaseFixture
		wantErr     bool
		errContains string
	}{
		{
			name: "success",
			fx:   releaseFixture{apiStatus: http.StatusOK, archive: archiveData},
		},
		{
			name:        "API error",
			fx:          releaseFixture{apiStatus: http.StatusInternalServerError},
			wantErr:     true,
			errContains: "fetch latest release",
		},
		{
			name:        "download fails",
			fx:          releaseFixture{apiStatus: http.StatusOK, checksums: "x  y\n"}, // server returns 404 for download
			wantErr:     true,
			errContains: "install update",
		},
		{
			name:        "checksum mismatch",
			fx:          releaseFixture{apiStatus: http.StatusOK, archive: archiveData, checksums: strings.Repeat("0", 64) + "  " + PlatformAssetName() + "\n"},
			wantErr:     true,
			errContains: "checksum mismatch",
		},
		{
			name:        "archive missing from checksums",
			fx:          releaseFixture{apiStatus: http.StatusOK, archive: archiveData, checksums: sha(archiveData) + "  some-other-file.tar.gz\n"},
			wantErr:     true,
			errContains: "no checksum for " + PlatformAssetName(),
		},
		{
			name:        "release without checksums.txt",
			fx:          releaseFixture{apiStatus: http.StatusOK, archive: archiveData, noChecksums: true},
			wantErr:     true,
			errContains: "cannot be verified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, installPath := fakeServer(t, tt.fx)
			if err := os.WriteFile(installPath, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}

			err := Run(runOpts(srv, installPath))
			got, readErr := os.ReadFile(installPath)
			if readErr != nil {
				t.Fatalf("read installed binary: %v", readErr)
			}
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("expected error containing %q, got: %v", tt.errContains, err)
				}
				// A refused download must leave the installed binary alone.
				if string(got) != "old" {
					t.Fatalf("installed binary changed to %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytes.Equal(got, fakeBinary) {
				t.Fatalf("installed binary content mismatch: got %q, want %q", got, fakeBinary)
			}
		})
	}
}

func TestChecksumFor(t *testing.T) {
	body := []byte("aaa111  smelt_Linux_x86_64.tar.gz\nBBB222  smelt_Darwin_arm64.tar.gz\n")
	got, err := checksumFor(body, "smelt_Darwin_arm64.tar.gz")
	if err != nil || got != "bbb222" {
		t.Fatalf("checksumFor = %q, %v", got, err)
	}
	if _, err := checksumFor(body, "smelt_Linux_arm64.tar.gz"); err == nil {
		t.Fatal("expected an error for a name that is not listed")
	}
}

func TestRunRefusesSourceBuilds(t *testing.T) {
	archive := makeTarGz(t, []byte("new"))
	srv, installPath := fakeServer(t, releaseFixture{apiStatus: http.StatusOK, archive: archive})
	if err := os.WriteFile(installPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("dev version", func(t *testing.T) {
		opts := runOpts(srv, installPath)
		opts.Version = "dev"
		err := Run(opts)
		if err == nil || !strings.Contains(err.Error(), "git pull && task build") {
			t.Fatalf("expected a source-build refusal, got %v", err)
		}
	})

	t.Run("symlinked install", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "smelt")
		if err := os.Symlink(installPath, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		err := Run(runOpts(srv, link))
		if err == nil || !strings.Contains(err.Error(), "git pull && task build") {
			t.Fatalf("expected a source-build refusal, got %v", err)
		}
		if got, _ := os.ReadFile(installPath); string(got) != "old" {
			t.Fatalf("the symlink's target was replaced: %q", got)
		}
	})
}

func TestIsBrewManaged(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/opt/homebrew/bin/smelt", true},
		{"/opt/homebrew/bin/something-else", true},
		// Callers resolve symlinks first, so the Cellar paths that
		// <prefix>/bin/smelt points at have to match too.
		{"/opt/homebrew/Cellar/smelt/1.2.3/bin/smelt", true},
		{"/usr/local/Cellar/smelt/1.2.3/bin/smelt", true},
		{"/home/linuxbrew/.linuxbrew/Cellar/smelt/1.2.3/bin/smelt", true},
		{"/usr/local/bin/smelt", false},
		{"/home/user/.local/bin/smelt", false},
		{"/tmp/smelt", false},
	}
	for _, tt := range tests {
		got := isBrewManaged(tt.path)
		if got != tt.want {
			t.Errorf("isBrewManaged(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestPlatformAssetName(t *testing.T) {
	name := PlatformAssetName()
	if !strings.HasPrefix(name, "smelt_") {
		t.Errorf("expected name to start with smelt_, got %q", name)
	}
	if !strings.HasSuffix(name, ".tar.gz") {
		t.Errorf("expected name to end with .tar.gz, got %q", name)
	}

	switch runtime.GOARCH {
	case "amd64":
		if !strings.Contains(name, "x86_64") {
			t.Errorf("expected x86_64 for amd64, got %q", name)
		}
	case "arm64":
		if !strings.Contains(name, "arm64") {
			t.Errorf("expected arm64, got %q", name)
		}
	}
}

// The archive name is spelled in three places: .goreleaser.yaml produces it,
// install.sh downloads it and this package expects it. Rendering the goreleaser
// template here is what keeps them from drifting apart.
func TestAssetNameMatchesGoreleaserTemplate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		ProjectName string `yaml:"project_name"`
		Archives    []struct {
			NameTemplate string `yaml:"name_template"`
		} `yaml:"archives"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectName != AssetPrefix {
		t.Fatalf("project_name = %q, want %q", cfg.ProjectName, AssetPrefix)
	}
	if len(cfg.Archives) == 0 {
		t.Fatal("no archives in .goreleaser.yaml")
	}
	title := func(s string) string { return strings.ToUpper(s[:1]) + s[1:] }
	tmpl, err := template.New("name").Funcs(template.FuncMap{"title": title}).Parse(cfg.Archives[0].NameTemplate)
	if err != nil {
		t.Fatal(err)
	}

	for _, goos := range []string{"linux", "darwin"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			var buf bytes.Buffer
			err := tmpl.Execute(&buf, map[string]string{"ProjectName": cfg.ProjectName, "Os": goos, "Arch": goarch, "Arm": ""})
			if err != nil {
				t.Fatal(err)
			}
			if got, want := assetName(goos, goarch), buf.String()+".tar.gz"; got != want {
				t.Errorf("assetName(%s, %s) = %q, goreleaser builds %q", goos, goarch, got, want)
			}
		}
	}
}

func TestRunRefusesBrewInstall(t *testing.T) {
	err := Run(Options{
		Out: io.Discard, Client: http.DefaultClient, APIBase: "https://example.invalid",
		InstallPath: "/opt/homebrew/Cellar/smelt/1.2.3/bin/smelt", Version: "v1.0.0",
	})
	if err == nil {
		t.Fatal("expected Run to refuse to replace a Homebrew-managed binary")
	}
	// Homebrew's smelt is another project's build; the way out is the installer,
	// never a brew command.
	if !strings.Contains(err.Error(), "Homebrew") || !strings.Contains(err.Error(), InstallCommand) || strings.Contains(err.Error(), "brew upgrade") {
		t.Errorf("expected the installer to be suggested, got %q", err.Error())
	}
}

// The release repository is named once; everything else derives from it.
func TestReleaseRepoIsDefinedOnce(t *testing.T) {
	if Repo != "smeltdev/smelt" {
		t.Fatalf("Repo = %q", Repo)
	}
	if got := ReleaseURL("v1.2.3"); got != "https://github.com/smeltdev/smelt/releases/tag/v1.2.3" {
		t.Errorf("ReleaseURL = %q", got)
	}
	if strings.Contains(InstallCommand, "CircleCI") {
		t.Errorf("InstallCommand points at CircleCI: %q", InstallCommand)
	}
}
