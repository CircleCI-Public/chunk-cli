package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// makeTarGz returns a .tar.gz containing a single file named "chunk" with the given content.
func makeTarGz(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	hdr := &tar.Header{
		Name: "chunk",
		Mode: 0o755,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeServer sets up an httptest server that serves a GitHub-like release API
// and a download endpoint. Returns the server and the install target path.
func fakeServer(t *testing.T, apiStatus int, assetContent []byte) (*httptest.Server, string) {
	t.Helper()

	installDir := t.TempDir()
	installPath := filepath.Join(installDir, "chunk")

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			if apiStatus != http.StatusOK {
				w.WriteHeader(apiStatus)
				return
			}
			assetName := PlatformAssetName()
			rel := ghRelease{
				TagName: "v9.9.9",
				Assets: []ghAsset{
					{
						Name:               assetName,
						BrowserDownloadURL: srv.URL + "/download/" + assetName,
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rel)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/download/") {
			if assetContent == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write(assetContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	return srv, installPath
}

func TestRun(t *testing.T) {
	fakeBinary := []byte("#!/bin/sh\necho chunk v9.9.9\n")
	archiveData := makeTarGz(t, fakeBinary)

	tests := []struct {
		name        string
		apiStatus   int
		archive     []byte
		wantErr     bool
		errContains string
	}{
		{
			name:      "success",
			apiStatus: http.StatusOK,
			archive:   archiveData,
		},
		{
			name:        "API error",
			apiStatus:   http.StatusInternalServerError,
			wantErr:     true,
			errContains: "fetch latest release",
		},
		{
			name:        "download fails",
			apiStatus:   http.StatusOK,
			archive:     nil, // server returns 404 for download
			wantErr:     true,
			errContains: "install update",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, installPath := fakeServer(t, tt.apiStatus, tt.archive)

			err := Run(io.Discard, srv.Client(), srv.URL, installPath)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("expected error containing %q, got: %v", tt.errContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got, err := os.ReadFile(installPath)
			if err != nil {
				t.Fatalf("read installed binary: %v", err)
			}
			if !bytes.Equal(got, fakeBinary) {
				t.Fatalf("installed binary content mismatch: got %q, want %q", got, fakeBinary)
			}
		})
	}
}

func TestIsBrewManaged(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/opt/homebrew/bin/chunk", true},
		{"/opt/homebrew/bin/something-else", true},
		{"/usr/local/bin/chunk", false},
		{"/home/user/.local/bin/chunk", false},
		{"/tmp/chunk", false},
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
	if !strings.HasPrefix(name, "chunk-cli_") {
		t.Errorf("expected name to start with chunk-cli_, got %q", name)
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
