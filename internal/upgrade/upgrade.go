package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	repoOwner      = "CircleCI-Public"
	repoName       = "chunk-cli"
	maxArchiveSize = 100 << 20 // 100 MB
)

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func isBrewManaged(path string) bool {
	return strings.Contains(path, "/opt/homebrew/bin/")
}

func Run(out io.Writer, client *http.Client, apiBase, installPath string) error {
	if isBrewManaged(installPath) {
		return fmt.Errorf("chunk is managed by Homebrew — upgrade with: brew upgrade chunk-cli")
	}

	rel, err := fetchLatestRelease(client, apiBase)
	if err != nil {
		return fmt.Errorf("fetch latest release: %w", err)
	}

	assetName := PlatformAssetName()
	var downloadURL string
	for _, a := range rel.Assets {
		if a.Name == assetName {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no release asset found for platform (%s)", assetName)
	}

	_, _ = fmt.Fprintf(out, "Downloading chunk %s...\n", rel.TagName)
	if err := downloadAndReplace(client, downloadURL, installPath); err != nil {
		return fmt.Errorf("install update: %w", err)
	}

	_, _ = fmt.Fprintf(out, "Updated to %s\n", rel.TagName)
	return nil
}

func fetchLatestRelease(client *http.Client, apiBase string) (*ghRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", apiBase, repoOwner, repoName)
	req, err := http.NewRequest(http.MethodGet, url, nil) //nolint:gosec // URL comes from trusted config (GITHUB_API_URL env var)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req) //nolint:gosec // same as above
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &rel, nil
}

func PlatformAssetName() string {
	osName := runtime.GOOS
	if len(osName) > 0 {
		osName = strings.ToUpper(osName[:1]) + osName[1:]
	}
	archName := runtime.GOARCH
	if archName == "amd64" {
		archName = "x86_64"
	}
	return fmt.Sprintf("chunk_%s_%s.tar.gz", osName, archName)
}

func downloadAndReplace(client *http.Client, url, installPath string) error {
	resp, err := client.Get(url) //nolint:gosec // URL comes from GitHub release API response for a known repo
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if filepath.Base(hdr.Name) != "chunk" {
			continue
		}

		dir := filepath.Dir(installPath)
		tmp, err := os.CreateTemp(dir, "chunk-*.tmp")
		if err != nil {
			return fmt.Errorf("create temp file: %w", err)
		}
		tmpPath := tmp.Name()

		if _, err := io.Copy(tmp, io.LimitReader(tr, maxArchiveSize)); err != nil { //nolint:gosec // size limited above
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("write binary: %w", err)
		}
		_ = tmp.Close()

		if err := os.Chmod(tmpPath, 0o755); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("chmod: %w", err)
		}

		if err := os.Rename(tmpPath, installPath); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("replace binary: %w", err)
		}
		return nil
	}

	return fmt.Errorf("chunk binary not found in archive")
}
