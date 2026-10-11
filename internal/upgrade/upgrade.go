package upgrade

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
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

// Where chunk's releases live. This is the one place that names the release
// repository: the update check, the release link, the upgrade itself and the
// installer script all derive from it.
const (
	// Repo is the GitHub "owner/name" releases are published to and downloaded from.
	Repo = "web3technologies/chunk-cli"

	// AssetPrefix is the goreleaser project name, which starts every archive name.
	AssetPrefix = "chunk"

	// ChecksumsAsset is the release asset listing every archive's SHA-256.
	ChecksumsAsset = "checksums.txt"

	// InstallScriptURL installs the latest release.
	InstallScriptURL = "https://raw.githubusercontent.com/" + Repo + "/main/install.sh"

	// InstallCommand is the one-liner that installs the latest release.
	InstallCommand = "curl -fsSL " + InstallScriptURL + " | bash"

	maxArchiveSize   = 100 << 20 // 100 MB
	maxChecksumsSize = 1 << 20   // 1 MB
)

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// brewPathFragments are path fragments that only appear inside a Homebrew
// prefix. Callers resolve symlinks before checking (os.Executable returns the
// symlink in <prefix>/bin, which resolves into the Cellar), so both the
// symlink and Cellar forms have to match. Bare <prefix>/bin is deliberately
// absent for the Intel macOS prefix: /usr/local/bin holds plenty of binaries
// Homebrew does not manage.
var brewPathFragments = []string{
	"/opt/homebrew/",              // Apple Silicon macOS prefix (bin and Cellar)
	"/usr/local/Cellar/",          // Intel macOS
	"/usr/local/Homebrew/",        // Intel macOS
	"/home/linuxbrew/.linuxbrew/", // Linuxbrew
}

func isBrewManaged(path string) bool {
	for _, fragment := range brewPathFragments {
		if strings.Contains(path, fragment) {
			return true
		}
	}
	return false
}

// ReleaseURL returns the GitHub release page for tag, where its changelog lives.
func ReleaseURL(tag string) string {
	return fmt.Sprintf("https://github.com/%s/releases/tag/%s", Repo, tag)
}

// SelfUpgradeCommand returns the command that upgrades the running binary. A
// Homebrew-managed chunk is not this project's build and must not be replaced
// in place, so it is pointed at the installer instead.
func SelfUpgradeCommand() string {
	execPath, err := os.Executable()
	if err != nil {
		return "chunk upgrade"
	}
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}
	if isBrewManaged(execPath) {
		return InstallCommand
	}
	return "chunk upgrade"
}

// Options configure Run.
type Options struct {
	Out    io.Writer
	Client *http.Client
	// APIBase is the GitHub API root.
	APIBase string
	// InstallPath is the binary to replace, as invoked: a symlink is not
	// resolved, because one marks a source build.
	InstallPath string
	// Version is the running version; "dev" marks a source build.
	Version string
}

// ErrSourceBuild is the refusal for a binary that came from `task build`.
var ErrSourceBuild = errors.New("source build")

// ErrHomebrewManaged is the refusal for a binary Homebrew installed, which is
// CircleCI's build rather than this project's.
var ErrHomebrewManaged = errors.New("managed by Homebrew")

func errSourceBuild(reason string) error {
	return fmt.Errorf("this chunk is a source build (%s), so it has no release to upgrade to: update it with: git pull && task build: %w", reason, ErrSourceBuild)
}

// checkReplaceable reports why the binary at path must not be replaced with a
// release, or nil when it may be.
func checkReplaceable(path, version string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	if isBrewManaged(resolved) {
		return fmt.Errorf("chunk at %s is managed by Homebrew, which installs a different build than this project's releases; install this one with: %s: %w", resolved, InstallCommand, ErrHomebrewManaged)
	}
	if version == "dev" {
		return errSourceBuild("version is dev")
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errSourceBuild(path + " is a symlink, as task dev-install makes")
	}
	return nil
}

// Run replaces the binary at opts.InstallPath with the latest release, after
// checking the archive against the release's checksums.txt.
func Run(opts Options) error {
	if err := checkReplaceable(opts.InstallPath, opts.Version); err != nil {
		return err
	}

	rel, err := fetchLatestRelease(opts.Client, opts.APIBase)
	if err != nil {
		return fmt.Errorf("fetch latest release: %w", err)
	}

	assetName := PlatformAssetName()
	archiveURL := rel.assetURL(assetName)
	if archiveURL == "" {
		return fmt.Errorf("no release asset found for platform (%s)", assetName)
	}
	checksumsURL := rel.assetURL(ChecksumsAsset)
	if checksumsURL == "" {
		return fmt.Errorf("release %s has no %s, so the download cannot be verified", rel.TagName, ChecksumsAsset)
	}

	_, _ = fmt.Fprintf(opts.Out, "Downloading chunk %s...\n", rel.TagName)
	if err := downloadAndReplace(opts.Client, archiveURL, checksumsURL, assetName, opts.InstallPath); err != nil {
		return fmt.Errorf("install update: %w", err)
	}

	_, _ = fmt.Fprintf(opts.Out, "Updated to %s\n", rel.TagName)
	return nil
}

func (r *ghRelease) assetURL(name string) string {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

func fetchLatestRelease(client *http.Client, apiBase string) (*ghRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", apiBase, Repo)
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

// PlatformAssetName is the release archive for the running platform.
func PlatformAssetName() string {
	return assetName(runtime.GOOS, runtime.GOARCH)
}

// assetName mirrors the archives.name_template in .goreleaser.yaml, which a
// test pins it to, and the name install.sh downloads.
func assetName(goos, goarch string) string {
	osName := goos
	if len(osName) > 0 {
		osName = strings.ToUpper(osName[:1]) + osName[1:]
	}
	if goarch == "amd64" {
		goarch = "x86_64"
	}
	return fmt.Sprintf("%s_%s_%s.tar.gz", AssetPrefix, osName, goarch)
}

// get fetches url and returns at most limit bytes, failing when there are more.
func get(client *http.Client, url string, limit int64) ([]byte, error) {
	resp, err := client.Get(url) //nolint:gosec // URL comes from the GitHub release API response for a known repo
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return data, nil
}

// checksumFor finds name's SHA-256 in a checksums.txt body ("<hex>  <name>").
func checksumFor(checksums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(checksums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum for %s in %s", name, ChecksumsAsset)
}

// verifiedArchive downloads the archive and returns it only if it matches the
// release's checksums.txt.
func verifiedArchive(client *http.Client, archiveURL, checksumsURL, name string) ([]byte, error) {
	checksums, err := get(client, checksumsURL, maxChecksumsSize)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", ChecksumsAsset, err)
	}
	want, err := checksumFor(checksums, name)
	if err != nil {
		return nil, err
	}
	archive, err := get(client, archiveURL, maxArchiveSize)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return nil, fmt.Errorf("checksum mismatch for %s (expected %s, got %s)", name, want, got)
	}
	return archive, nil
}

func downloadAndReplace(client *http.Client, archiveURL, checksumsURL, name, installPath string) error {
	archive, err := verifiedArchive(client, archiveURL, checksumsURL, name)
	if err != nil {
		return err
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
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
		// Match the archive root exactly, not by base name: nothing stops the
		// archive shipping a nested file called "chunk" ahead of the binary.
		if hdr.Typeflag != tar.TypeReg || filepath.Clean(hdr.Name) != "chunk" {
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
