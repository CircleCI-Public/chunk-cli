package envbuilder

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

const (
	stackPython     = "python"
	stackGo         = "go"
	stackJavaScript = "javascript"
	stackTypeScript = "typescript"
	stackRust       = "rust"
	stackJava       = "java"
	stackRuby       = "ruby"
	stackPHP        = "php"
	stackElixir     = "elixir"
	stackDotNet     = "dotnet"
	stackCPP        = "cpp"
	stackDart       = "dart"
	stackScala      = "scala"
	stackHaskell    = "haskell"
	stackUnknown    = "unknown"

	cimgPrefix  = "cimg/"
	cmdDartTest = "dart test"
	toolPytest  = "pytest"
)

// Environment describes the detected tech stack and build configuration for a repository.
type Environment struct {
	Stack        string   `json:"stack"`
	Install      string   `json:"install"`
	Test         string   `json:"test"`
	SystemDeps   []string `json:"system_deps"`
	Image        string   `json:"image"`
	ImageVersion string   `json:"image_version"`
}

func fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

var indicatorFiles = map[string]string{
	"pyproject.toml":   stackPython,
	"setup.py":         stackPython,
	"requirements.txt": stackPython,
	"Pipfile":          stackPython,
	"go.mod":           stackGo,
	"package.json":     stackJavaScript,
	"tsconfig.json":    stackTypeScript,
	"Cargo.toml":       stackRust,
	"pom.xml":          stackJava,
	"build.gradle":     stackJava,
	"build.sbt":        stackScala,
	"Gemfile":          stackRuby,
	"composer.json":    stackPHP,
	"mix.exs":          stackElixir,
	"CMakeLists.txt":   stackCPP,
	"meson.build":      stackCPP,
	"pubspec.yaml":     stackDart,
	"stack.yaml":       stackHaskell,
	"cabal.project":    stackHaskell,
}

var sourceExtensions = map[string]string{
	".py":     stackPython,
	".go":     stackGo,
	".js":     stackJavaScript,
	".ts":     stackTypeScript,
	".rs":     stackRust,
	".java":   stackJava,
	".scala":  stackScala,
	".sbt":    stackScala,
	".rb":     stackRuby,
	".php":    stackPHP,
	".ex":     stackElixir,
	".exs":    stackElixir,
	".cs":     stackDotNet,
	".csproj": stackDotNet,
	".sln":    stackDotNet,
	".cpp":    stackCPP,
	".cc":     stackCPP,
	".cxx":    stackCPP,
	".hpp":    stackCPP,
	".hxx":    stackCPP,
	".dart":   stackDart,
	".hs":     stackHaskell,
	".lhs":    stackHaskell,
}

var skipDirs = map[string]bool{
	".git":         true,
	".venv":        true,
	"node_modules": true,
	"vendor":       true,
}

var circleciImages = map[string]string{
	stackPython:     cimgPrefix + "python",
	stackGo:         cimgPrefix + "go",
	stackJavaScript: cimgPrefix + "node",
	stackTypeScript: cimgPrefix + "node",
	stackRust:       cimgPrefix + "rust",
	stackJava:       cimgPrefix + "openjdk",
	stackScala:      cimgPrefix + "openjdk",
	stackRuby:       cimgPrefix + "ruby",
	stackPHP:        cimgPrefix + "php",
	stackElixir:     "elixir",
	stackDotNet:     "mcr.microsoft.com/dotnet/sdk",
	stackCPP:        cimgPrefix + "base",
	stackDart:       "dart",
	stackHaskell:    "haskell",
}

var versionTagRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

// elixirVersionInLineRe matches a bare major.minor version number (e.g. "1.18")
// within a single line of text.
var elixirVersionInLineRe = regexp.MustCompile(`\b(\d+\.\d+)\b`)

// elixirOTPTagRe matches Elixir Docker image tags that bundle a specific OTP
// (Erlang/OTP) major version, e.g. "1.18.4-otp-26".
var elixirOTPTagRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)-otp-(\d+)$`)

// otpMajorInLineRe matches a two-digit Erlang/OTP major version number
// (e.g. "26", "27") appearing as a standalone word in a CI matrix line.
var otpMajorInLineRe = regexp.MustCompile(`\b([2-9]\d)\b`)

// cimgBaseDateTagRe matches the date-based tags used by cimg/base (and cimg/
// images generally), which are of the form "YYYY.MM" or "YYYY.MM.N"
// (e.g. "2024.01", "2024.01.1"). These do not conform to semver so they cannot
// be matched by versionTagRe.
var cimgBaseDateTagRe = regexp.MustCompile(`^(\d{4})\.(\d{2})(?:\.(\d+))?$`)

var dockerHubClient = httpcl.New(httpcl.Config{})

type dockerHubTagsResponse struct {
	Results []struct {
		Name string `json:"name"`
	} `json:"results"`
	Next string `json:"next"`
}

func fetchAllImageVersions(ctx context.Context, client *httpcl.Client, image string) ([]string, error) {
	// Official Docker Hub images (e.g. "elixir", "ubuntu") have no namespace
	// slash; they live under the "library" namespace in the Docker Hub API.
	apiImage := image
	if !strings.Contains(image, "/") {
		apiImage = "library/" + image
	}

	parts := strings.SplitN(apiImage, "/", 2)

	var allTags []string
	route := fmt.Sprintf(
		"https://hub.docker.com/v2/repositories/%s/%s/tags?page_size=100&ordering=last_updated",
		parts[0], parts[1],
	)

	for route != "" && len(allTags) < 300 {
		var page dockerHubTagsResponse
		if _, err := client.Call(ctx, httpcl.NewRequest("GET", route, httpcl.JSONDecoder(&page))); err != nil {
			return nil, fmt.Errorf("docker hub request failed: %w", err)
		}

		for _, tag := range page.Results {
			if versionTagRe.MatchString(tag.Name) {
				allTags = append(allTags, tag.Name)
			}
		}
		route = page.Next
	}

	if len(allTags) == 0 {
		return nil, fmt.Errorf("no version tags found for %s", image)
	}

	return allTags, nil
}

func fetchLatestImageVersion(ctx context.Context, client *httpcl.Client, image string) (string, error) {
	allTags, err := fetchAllImageVersions(ctx, client, image)
	if err != nil {
		return "", err
	}
	return highestVersion(allTags), nil
}

func fetchLatestImageVersionWithConstraint(ctx context.Context, client *httpcl.Client, image string, maxMajor int) (string, error) {
	allTags, err := fetchAllImageVersions(ctx, client, image)
	if err != nil {
		return "", err
	}

	var filtered []string
	for _, tag := range allTags {
		parts := strings.Split(tag, ".")
		if len(parts) >= 1 {
			major, err := strconv.Atoi(parts[0])
			if err == nil && major <= maxMajor {
				filtered = append(filtered, tag)
			}
		}
	}

	if len(filtered) == 0 {
		return highestVersion(allTags), nil
	}

	return highestVersion(filtered), nil
}

// fetchLatestImageVersionWithMajorMinorConstraint returns the highest version tag
// whose major.minor is no greater than maxMajor.maxMinor. This is used to cap
// language runtimes at a known-compatible minor release (e.g. Python 3.13 when
// a dependency like uvloop does not yet support 3.14+).
func fetchLatestImageVersionWithMajorMinorConstraint(ctx context.Context, client *httpcl.Client, image string, maxMajor, maxMinor int) (string, error) {
	allTags, err := fetchAllImageVersions(ctx, client, image)
	if err != nil {
		return "", err
	}

	var filtered []string
	for _, tag := range allTags {
		parts := strings.Split(tag, ".")
		if len(parts) >= 2 {
			major, err1 := strconv.Atoi(parts[0])
			minor, err2 := strconv.Atoi(parts[1])
			if err1 == nil && err2 == nil {
				if major < maxMajor || (major == maxMajor && minor <= maxMinor) {
					filtered = append(filtered, tag)
				}
			}
		}
	}

	if len(filtered) == 0 {
		return highestVersion(allTags), nil
	}

	return highestVersion(filtered), nil
}

// fetchElixirOTPImageVersion returns the highest Elixir Docker image tag of the
// form "X.Y.Z-otp-N" whose Elixir major.minor is at most maxMajor.maxMinor and
// whose bundled OTP major equals otpMajor (e.g. "1.18.4-otp-26"). Using the
// minimum OTP version from CI avoids picking up OTP 27's changed calendar/timer
// behaviour that breaks test suites written against OTP 26.
// Falls back to fetchLatestImageVersionWithMajorMinorConstraint when no matching
// OTP-specific tag is found.
func fetchElixirOTPImageVersion(ctx context.Context, client *httpcl.Client, image string, maxMajor, maxMinor, otpMajor int) (string, error) {
	apiImage := image
	if !strings.Contains(image, "/") {
		apiImage = "library/" + image
	}
	parts := strings.SplitN(apiImage, "/", 2)

	var allTags []string
	route := fmt.Sprintf(
		"https://hub.docker.com/v2/repositories/%s/%s/tags?page_size=100&ordering=last_updated",
		parts[0], parts[1],
	)
	for route != "" && len(allTags) < 300 {
		var page dockerHubTagsResponse
		if _, err := client.Call(ctx, httpcl.NewRequest("GET", route, httpcl.JSONDecoder(&page))); err != nil {
			return "", fmt.Errorf("docker hub request failed: %w", err)
		}
		for _, tag := range page.Results {
			allTags = append(allTags, tag.Name)
		}
		route = page.Next
	}

	// Find the highest Elixir version tag matching the OTP constraint.
	bestTag := ""
	bestMaj, bestMin, bestPatch := 0, 0, 0
	for _, tag := range allTags {
		m := elixirOTPTagRe.FindStringSubmatch(tag)
		if m == nil {
			continue
		}
		major, err1 := strconv.Atoi(m[1])
		minor, err2 := strconv.Atoi(m[2])
		patch, err3 := strconv.Atoi(m[3])
		otp, err4 := strconv.Atoi(m[4])
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		if otp != otpMajor {
			continue
		}
		// Enforce the Elixir major.minor upper bound.
		if major > maxMajor || (major == maxMajor && minor > maxMinor) {
			continue
		}
		// Keep the highest Elixir patch within bounds.
		if major > bestMaj || (major == bestMaj && minor > bestMin) || (major == bestMaj && minor == bestMin && patch > bestPatch) {
			bestMaj, bestMin, bestPatch = major, minor, patch
			bestTag = tag
		}
	}

	if bestTag != "" {
		return bestTag, nil
	}

	// Fall back to a plain version tag if no OTP-specific one was found.
	return fetchLatestImageVersionWithMajorMinorConstraint(ctx, client, image, maxMajor, maxMinor)
}

// fetchLatestCimgBaseDateVersion fetches the most recent cimg/base tag, which
// uses a "YYYY.MM" or "YYYY.MM.N" date format rather than semver.
func fetchLatestCimgBaseDateVersion(ctx context.Context, client *httpcl.Client, image string) (string, error) {
	apiImage := image
	if !strings.Contains(image, "/") {
		apiImage = "library/" + image
	}
	parts := strings.SplitN(apiImage, "/", 2)

	var dateTags []string
	route := fmt.Sprintf(
		"https://hub.docker.com/v2/repositories/%s/%s/tags?page_size=100&ordering=last_updated",
		parts[0], parts[1],
	)

	for route != "" && len(dateTags) < 300 {
		var page dockerHubTagsResponse
		if _, err := client.Call(ctx, httpcl.NewRequest("GET", route, httpcl.JSONDecoder(&page))); err != nil {
			return "", fmt.Errorf("docker hub request failed: %w", err)
		}
		for _, tag := range page.Results {
			if cimgBaseDateTagRe.MatchString(tag.Name) {
				dateTags = append(dateTags, tag.Name)
			}
		}
		route = page.Next
	}

	if len(dateTags) == 0 {
		return "", fmt.Errorf("no version tags found for %s", image)
	}

	// Return the lexicographically (and chronologically) highest date tag.
	best := dateTags[0]
	for _, tag := range dateTags[1:] {
		if compareCimgBaseDateTags(tag, best) > 0 {
			best = tag
		}
	}
	return best, nil
}

// compareCimgBaseDateTags compares two cimg/base date tags of the form
// "YYYY.MM" or "YYYY.MM.N".  Returns positive when a > b, negative when a < b,
// and 0 when they are equal.
func compareCimgBaseDateTags(a, b string) int {
	ma := cimgBaseDateTagRe.FindStringSubmatch(a)
	mb := cimgBaseDateTagRe.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return 0
	}
	// Groups: [1]=year [2]=month [3]=patch (empty string when absent → Atoi → 0)
	for i := 1; i <= 3; i++ {
		va, _ := strconv.Atoi(ma[i])
		vb, _ := strconv.Atoi(mb[i])
		if va != vb {
			return va - vb
		}
	}
	return 0
}

func highestVersion(tags []string) string {
	best := tags[0]
	for _, tag := range tags[1:] {
		if compareVersions(tag, best) > 0 {
			best = tag
		}
	}
	return best
}

func compareVersions(a, b string) int {
	ma := versionTagRe.FindStringSubmatch(a)
	mb := versionTagRe.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return 0 // malformed version — treated as equal
	}
	for i := range 3 {
		na, _ := strconv.Atoi(ma[i+1])
		nb, _ := strconv.Atoi(mb[i+1])
		if na != nb {
			return na - nb
		}
	}
	return 0
}

var extraDepInstalls = map[string]string{
	"uv":          "pip install uv",
	"pipenv":      "pip install pipenv",
	"yarn":        "sudo npm install -g yarn",
	"pnpm":        "sudo npm install -g pnpm",
	"mvn":         "curl -fsSL https://archive.apache.org/dist/maven/maven-3/3.9.6/binaries/apache-maven-3.9.6-bin.tar.gz | sudo tar -xz -C /opt && sudo ln -s /opt/apache-maven-3.9.6/bin/mvn /usr/local/bin/mvn",
	"sbt":         "curl -fsSL https://github.com/sbt/sbt/releases/download/v1.10.7/sbt-1.10.7.tgz | sudo tar -xz -C /usr/local && sudo ln -sf /usr/local/sbt/bin/sbt /usr/local/bin/sbt",
	"composer":    "php -r \"copy('https://getcomposer.org/installer', 'composer-setup.php');\" && sudo php composer-setup.php --install-dir=/usr/local/bin --filename=composer && php -r \"unlink('composer-setup.php');\"",
	"git":         "apt-get update && apt-get install -y git --no-install-recommends && rm -rf /var/lib/apt/lists/*",
	"dart-sass":   "apt-get update && apt-get install -y curl --no-install-recommends && rm -rf /var/lib/apt/lists/* && ARCH=$(uname -m) && case \"$ARCH\" in x86_64) SASS_ARCH=linux-x64 ;; aarch64) SASS_ARCH=linux-arm64 ;; *) echo \"Unsupported arch: $ARCH\" && exit 1 ;; esac && curl -fsSL \"https://github.com/sass/dart-sass/releases/download/1.80.3/dart-sass-1.80.3-${SASS_ARCH}.tar.gz\" | sudo tar -xz -C /usr/local && sudo chmod -R 755 /usr/local/dart-sass && printf '#!/bin/sh\\nexec /usr/local/dart-sass/sass \"$@\"\\n' | sudo tee /usr/local/bin/sass > /dev/null && sudo chmod +x /usr/local/bin/sass",
	"asciidoctor": "apt-get update && apt-get install -y asciidoctor --no-install-recommends && rm -rf /var/lib/apt/lists/*",
	"pandoc":      "apt-get update && apt-get install -y pandoc --no-install-recommends && rm -rf /var/lib/apt/lists/*",
	"rst2html":    "apt-get update && apt-get install -y python3-docutils --no-install-recommends && rm -rf /var/lib/apt/lists/*",
	// locales installs the locales package and generates common locales (e.g. fr_FR.UTF-8)
	// required by i18n-heavy PHP libraries such as Carbon.
	"locales": "apt-get update && apt-get install -y locales --no-install-recommends && locale-gen fr_FR.UTF-8 && update-locale && rm -rf /var/lib/apt/lists/*",
	// tzdata provides the IANA timezone database (America/Toronto, etc.) required
	// by PHP date libraries such as Carbon that call date_default_timezone_set().
	// DEBIAN_FRONTEND=noninteractive suppresses the interactive timezone prompt.
	"tzdata": "DEBIAN_FRONTEND=noninteractive apt-get update && apt-get install -y tzdata --no-install-recommends && rm -rf /var/lib/apt/lists/*",
	// faketime intercepts time() / gettimeofday() via LD_PRELOAD so that phpunit
	// always sees a fixed, DST-safe date.  PHP date libraries such as Carbon freeze
	// Carbon::now() to the real system time in setUp(); when that time lands on a
	// DST boundary (e.g. exactly 3 weeks after the America/Toronto spring-forward)
	// DateTime::diff() returns a DST-adjusted 20d 23h instead of the expected 21d,
	// causing time-sensitive tests to fail.  Pinning to mid-June keeps both "now"
	// and "now - 3 weeks" firmly inside the same DST offset (EDT, UTC-4) so the
	// diff is always exactly 21 calendar days.
	"faketime": "apt-get update && apt-get install -y faketime --no-install-recommends && rm -rf /var/lib/apt/lists/*",
	// Additional JDK toolchain versions required by Gradle projects that use
	// multiple toolchains (e.g. compiling module-info.java with an older JDK).
	// All extra JDKs are downloaded from Adoptium/Temurin rather than via apt
	// because the apt packages (openjdk-N-jdk) trigger ca-certificates-java
	// post-install hook failures when installed alongside an existing Java 21
	// installation (as found in cimg/openjdk:21.x).  openjdk-8-jdk is also
	// unavailable on aarch64 Ubuntu 22.04 repos.
	"openjdk-8":  "ARCH=$(uname -m) && case \"$ARCH\" in x86_64) JDK_ARCH=x64 ;; aarch64) JDK_ARCH=aarch64 ;; *) echo \"Unsupported arch: $ARCH\" && exit 1 ;; esac && curl -fsSL \"https://api.adoptium.net/v3/binary/latest/8/ga/linux/${JDK_ARCH}/jdk/hotspot/normal/eclipse\" -o /tmp/jdk8.tar.gz && sudo mkdir -p /usr/lib/jvm/java-8-temurin && sudo tar -xzf /tmp/jdk8.tar.gz -C /usr/lib/jvm/java-8-temurin --strip-components=1 && rm /tmp/jdk8.tar.gz",
	"openjdk-11": "ARCH=$(uname -m) && case \"$ARCH\" in x86_64) JDK_ARCH=x64 ;; aarch64) JDK_ARCH=aarch64 ;; *) echo \"Unsupported arch: $ARCH\" && exit 1 ;; esac && curl -fsSL \"https://api.adoptium.net/v3/binary/latest/11/ga/linux/${JDK_ARCH}/jdk/hotspot/normal/eclipse\" -o /tmp/jdk11.tar.gz && sudo mkdir -p /usr/lib/jvm/java-11-temurin && sudo tar -xzf /tmp/jdk11.tar.gz -C /usr/lib/jvm/java-11-temurin --strip-components=1 && rm /tmp/jdk11.tar.gz",
	"openjdk-17": "ARCH=$(uname -m) && case \"$ARCH\" in x86_64) JDK_ARCH=x64 ;; aarch64) JDK_ARCH=aarch64 ;; *) echo \"Unsupported arch: $ARCH\" && exit 1 ;; esac && curl -fsSL \"https://api.adoptium.net/v3/binary/latest/17/ga/linux/${JDK_ARCH}/jdk/hotspot/normal/eclipse\" -o /tmp/jdk17.tar.gz && sudo mkdir -p /usr/lib/jvm/java-17-temurin && sudo tar -xzf /tmp/jdk17.tar.gz -C /usr/lib/jvm/java-17-temurin --strip-components=1 && rm /tmp/jdk17.tar.gz",
}

// dockerfileContent generates the Dockerfile.test content for the given environment.
func dockerfileContent(dir string, env *Environment) string { //nolint:gocyclo
	var sb strings.Builder

	sb.WriteString("FROM " + env.Image + ":" + env.ImageVersion + "\n")

	for _, dep := range env.SystemDeps {
		if cmd, ok := extraDepInstalls[dep]; ok {
			// cimg/* images run as a non-root user (circleci), so apt-get requires sudo.
			if strings.HasPrefix(env.Image, cimgPrefix) {
				cmd = strings.ReplaceAll(cmd, "apt-get", "sudo apt-get")
				cmd = strings.ReplaceAll(cmd, "rm -rf /var/lib/apt/lists/*", "sudo rm -rf /var/lib/apt/lists/*")
				cmd = strings.ReplaceAll(cmd, "locale-gen", "sudo locale-gen")
				cmd = strings.ReplaceAll(cmd, "update-locale", "sudo update-locale")
			}
			sb.WriteString("\nRUN " + cmd + "\n")
		}
	}

	// When a Python project has Rust workspace members (e.g. maturin extensions like
	// pydantic-core), the install step compiles Rust code twice: once for the root
	// sync and once for the per-member sync.  Rust build artifacts written to
	// /app/<member>/target/ can exhaust Docker layer disk space before the second
	// compilation finishes ("No space left on device").  Redirecting CARGO_TARGET_DIR
	// to /tmp lets both compilations share a single target directory and enables
	// incremental builds, dramatically reducing space usage.
	// UV_CACHE_DIR is also redirected to /tmp because uv writes large temporary
	// build files (e.g. wheel builds for Rust extensions) to its cache under the
	// home directory (~/.cache/uv/builds-v0/), which can also exhaust the home
	// partition.  /tmp is not subject to the same size constraints in Docker.
	if env.Stack == stackPython && hasRustWorkspaceMember(dir) {
		sb.WriteString("\nENV CARGO_TARGET_DIR=/tmp/cargo-target\n")
		sb.WriteString("ENV UV_CACHE_DIR=/tmp/uv-cache\n")
	}

	// For Go projects, embed the module name in the WORKDIR path so that
	// tests which inspect their working directory (e.g. Hugo's codegen package
	// panics if strings.Contains(cwd, "<module>") is false) can find the
	// expected path component regardless of the host layout.
	//
	// For Ruby projects on cimg/* images, use /home/circleci/project to match
	// the actual CircleCI default working directory.  Some Ruby libraries (e.g.
	// rubocop itself) call PathUtil.smart_path() which relativises any path
	// that falls under Dir.pwd.  Their test suites hardcode expected paths as
	// "/app/.rubocop.yml" etc., so running under WORKDIR /app causes
	// smart_path to strip the prefix and the assertions fail.  Using
	// /home/circleci/project keeps /app/... paths outside Dir.pwd so
	// smart_path leaves them as absolute paths, matching the hardcoded
	// expectations.
	workdir := "/app"
	if env.Stack == stackGo {
		if modName := detectGoModuleName(dir); modName != "" {
			workdir = "/app/" + modName
		}
	} else if env.Stack == stackRuby && strings.HasPrefix(env.Image, cimgPrefix) {
		workdir = "/home/circleci/project"
	}
	sb.WriteString("\nWORKDIR " + workdir + "\n")
	// For Go, use the split-COPY pattern to maximise Docker layer cache hits
	// on the expensive "go mod download" step.
	//
	// The test harness writes env.json and Dockerfile.test into the repo
	// directory between iterations, and these files are part of the "COPY . ."
	// build context.  A monolithic "COPY . . && RUN go mod download" means that
	// any change to Dockerfile.test or env.json (which happens every time we
	// tweak the generated Dockerfile) invalidates the download layer, forcing a
	// full re-download on a disk that is already near capacity from accumulated
	// Docker layer history.
	//
	// By copying only go.mod and go.sum first, the download layer is keyed
	// solely on the dependency manifests.  As long as the project's deps do not
	// change, the layer is reused across iterations regardless of what else
	// changes in the build context.  The full source is then copied in a
	// separate step after the download completes.
	switch env.Stack {
	case stackGo:
		chown := ""
		if strings.HasPrefix(env.Image, cimgPrefix) {
			chown = "--chown=circleci:circleci "
		}
		sb.WriteString("COPY " + chown + "go.mod go.sum ./\n")
		sb.WriteString("RUN " + env.Install + "\n")
		sb.WriteString("\nCOPY " + chown + ". .\n")
	case stackRuby:
		// Use the split-COPY pattern (mirrors the Go approach) to avoid
		// exhausting disk space: copying the full source first and then
		// running "bundle install" leaves no room for Bundler to download
		// the compact_index metadata on constrained CI machines.  By
		// copying only the dependency manifests first the install layer has
		// the disk budget it needs.
		chown := ""
		if strings.HasPrefix(env.Image, cimgPrefix) {
			chown = "--chown=circleci:circleci "
		}
		// Always include Gemfile; add Gemfile.lock when present; include any
		// *.gemspec files because many gems have a Gemfile that calls
		// `gemspec`, which requires the .gemspec to be on disk during
		// `bundle install`.
		depFiles := "Gemfile"
		if fileExists(dir, "Gemfile.lock") {
			depFiles += " Gemfile.lock"
		}
		hasGemspec := false
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".gemspec") {
					depFiles += " " + e.Name()
					hasGemspec = true
				}
			}
		}
		sb.WriteString("COPY " + chown + depFiles + " ./\n")
		// Gem repos commonly do `require_relative 'lib/gem_name/version'`
		// inside their gemspec to read the VERSION constant.  When that
		// file is absent Bundler cannot parse the gemspec and aborts.
		// Copy the lib/ directory as a second step so the require succeeds
		// while the bulk of the source tree (spec/, docs/, …) is still
		// withheld until after the install.
		if hasGemspec && fileExists(dir, "lib") {
			sb.WriteString("COPY " + chown + "lib/ lib/\n")
		}
		sb.WriteString("RUN " + env.Install + "\n")
		sb.WriteString("\nCOPY " + chown + ". .\n")
	default:
		if strings.HasPrefix(env.Image, cimgPrefix) {
			sb.WriteString("COPY --chown=circleci:circleci . .\n")
		} else {
			sb.WriteString("COPY . .\n")
		}
	}
	// For .NET projects, the dotnet/sdk:N image on ARM64 only ships the N.0
	// runtime — it does NOT bundle previous runtimes (contrary to the AMD64
	// behaviour).  When global.json forces SDK version N but the test project
	// only targets net(N-1).0 (or lower), the compiled test-host binary cannot
	// execute because its required runtime is absent.  Detect this mismatch and
	// install the missing runtime via apt-get before running the restore/test
	// steps.
	if env.Stack == stackDotNet {
		// Use Windows-compatible NLS globalization instead of the Linux ICU
		// library.  Many .NET repos are developed and tested exclusively on
		// Windows; their test suites rely on Windows locale data (e.g. the
		// en-NZ culture uses a regular space before the AM/PM designator in
		// the Windows "F" FullDateTimePattern, whereas ICU 72+ switched to
		// NARROW NO-BREAK SPACE / U+202F).  Setting DOTNET_SYSTEM_GLOBALIZATION_USENLS=1
		// makes the .NET 8+ runtime use the embedded Windows-derived NLS data
		// on Linux, keeping locale output consistent with what the tests expect.
		sb.WriteString("\nENV DOTNET_SYSTEM_GLOBALIZATION_USENLS=1\n")

		sdkRe := regexp.MustCompile(`^(\d+)\.`)
		tfmRe := regexp.MustCompile(`--framework net(\d+)\.0`)
		if sdkM := sdkRe.FindStringSubmatch(env.ImageVersion); len(sdkM) == 2 {
			if tfmM := tfmRe.FindStringSubmatch(env.Test); len(tfmM) == 2 {
				sdkMajor, sdkErr := strconv.Atoi(sdkM[1])
				tfmMajor, tfmErr := strconv.Atoi(tfmM[1])
				if sdkErr == nil && tfmErr == nil && sdkMajor > tfmMajor && tfmMajor >= 6 {
					// dotnet-runtime-X.0 is not present in Debian's default
					// package repos; it requires the Microsoft package feed.
					// Using the official dotnet-install.sh script is simpler,
					// works on both ARM64 and AMD64, and installs directly into
					// /usr/share/dotnet where the SDK image already keeps its
					// runtimes.
					fmt.Fprintf(&sb,
						"\nRUN curl -sSL https://dot.net/v1/dotnet-install.sh | bash -s -- --runtime dotnet --channel %d.0 --install-dir /usr/share/dotnet\n",
						tfmMajor,
					)
				}
			}
		}
	}

	// Gradle projects on cimg images run as the circleci user.  During
	// `docker build` the Gradle wrapper tries to create its distribution
	// cache under ~/.gradle/wrapper/dists/, but /home/circleci may not be
	// writable in the Docker build context.  Redirect GRADLE_USER_HOME to
	// /tmp so the wrapper can always unpack and lock its distribution files.
	if env.Stack == stackJava && strings.Contains(env.Install, "gradlew") {
		sb.WriteString("\nENV GRADLE_USER_HOME=/tmp/.gradle\n")
	}

	// Go and Ruby already emitted their install steps inside the split-COPY
	// block above.
	if env.Stack != stackGo && env.Stack != stackRuby {
		sb.WriteString("\nRUN " + env.Install + "\n")
	}

	// Elixir-specific fixups applied after deps are fetched:
	//
	// tzdata IANA database version.
	//    tzdata 1.1.1+ bundles IANA tzdb 2021e+ which includes the 2021b revision
	//    that corrected historical European LMT offsets (e.g. Europe/Copenhagen
	//    changed from +00:50:20 to +00:53:28).  Tests written against the pre-
	//    2021b data expect +00:50 and fail with the newer bundled database.
	//
	//    When mix.lock pins tzdata >= 1.1.1 we download the pre-2021b ETS file
	//    (2020e.v2.ets) directly from the tzdata 1.1.0 hex package on repo.hex.pm,
	//    using Elixir's built-in :httpc and :erl_tar modules — no mix project or
	//    third-party HTTP client required.  The file is placed in a standalone
	//    /tz_data/release_ets/ directory and tzdata is configured to read from
	//    /tz_data via :data_dir.  Autoupdate is disabled so tzdata never fetches
	//    a newer IANA release at runtime.
	//
	//    When mix.lock pins tzdata < 1.1.1 (e.g. 1.0.3) the bundled data is
	//    already pre-2021b so no fixup is needed.
	if env.Stack == stackElixir {
		sb.WriteString("\nRUN if grep -q ':tzdata' mix.exs 2>/dev/null && " +
			`grep -q '"1\.1\.[1-9]' mix.lock 2>/dev/null; then ` +
			`elixir -e ':inets.start(); :ssl.start(); url = ~c"https://repo.hex.pm/tarballs/tzdata-1.1.0.tar"; {:ok, {{_, 200, _}, _, body}} = :httpc.request(:get, {url, []}, [{:timeout, 30000}, {:ssl, [{:verify, :verify_none}]}], []); {:ok, [{_, gz}]} = :erl_tar.extract({:binary, IO.iodata_to_binary(body)}, [:memory, {:files, [~c"contents.tar.gz"]}]); {:ok, [{_, data}]} = :erl_tar.extract({:binary, gz}, [:memory, :compressed, {:files, [~c"priv/release_ets/2020e.v2.ets"]}]); File.mkdir_p!(~c"/tz_data/release_ets"); File.write!(~c"/tz_data/release_ets/2020e.v2.ets", data)'; ` +
			"fi\n")
		sb.WriteString("\nRUN if grep -q ':tzdata' mix.exs 2>/dev/null && " +
			`grep -q '"1\.1\.[1-9]' mix.lock 2>/dev/null; then ` +
			"mkdir -p config && " +
			`printf '\nimport Config\nconfig :tzdata, :autoupdate, :disabled\nconfig :tzdata, :data_dir, "/tz_data"\n' >> config/runtime.exs; ` +
			"fi\n")
	}

	// Use shell form for CMD to allow proper shell quoting/expansion.
	//
	// For Go projects, use per-package invocations instead of a single
	// "go test ./..." to avoid GOTMPDIR exhaustion.  A single "./..."
	// invocation keeps every b<n>/ temp dir in GOTMPDIR alive for the
	// entire run — they are not cleaned up incrementally — so for large
	// projects with many packages the peak GOTMPDIR footprint can exhaust
	// the container's writable overlay layer.  Individual "go test <pkg>"
	// invocations clean up their GOTMPDIR when each process exits, bounding
	// peak disk usage to one package's worth of temp files at a time.
	//
	// Dependency-ordering is critical: "go list ./..." outputs packages in
	// alphabetical order, which puts the root package first (e.g.
	// "github.com/gohugoio/hugo" < "github.com/gohugoio/hugo/common/...").
	// The root package imports everything, so testing it first forces Go to
	// compile all 700+ transitive dependencies simultaneously into GOTMPDIR,
	// exhausting disk space before the test even runs.
	//
	// "go list -deps ./..." outputs packages in topological post-order
	// (dependencies before the packages that depend on them).  By filtering
	// that list to only the module's own testable packages we get a run order
	// where every leaf package is tested first.  GOCACHE warms incrementally
	// — each package adds only its own newly-compiled deps — so that by the
	// time the root package is reached all its deps are already cached and
	// the GOTMPDIR footprint for that final invocation is tiny (just the
	// linked test binary).
	//
	// NOTE: do NOT add a pre-build RUN step that populates GOCACHE here.
	// Although pre-building test-variant archives into GOCACHE and baking
	// them into the image avoids GOTMPDIR writes at runtime, the GOCACHE
	// for a large project (e.g. Hugo) grows to several GB.  Docker's layer
	// export then fails with "no space left on device" when the host's
	// containerd storage cannot accommodate the oversized layer.  The
	// per-package CMD loop below achieves the same GOTMPDIR bound without
	// inflating the image.
	if env.Stack == stackGo {
		// Build a dep-ordered list of this module's testable packages, then
		// test them one at a time.  $$ is the shell PID, used to give the
		// temp file a unique name so concurrent runs don't collide.
		sb.WriteString("\nCMD go list ./... > /tmp/mod-pkgs-$$ && go list -deps ./... | grep -Fxf /tmp/mod-pkgs-$$ | while IFS= read -r pkg; do go test \"$pkg\" || exit 1; done\n")
	} else {
		sb.WriteString("\nCMD " + env.Test + "\n")
	}

	return sb.String()
}

// WriteDockerfile writes Dockerfile.test (and Dockerfile.test.dockerignore if needed)
// to dir and returns the path to the written Dockerfile.
func WriteDockerfile(dir string, env *Environment) (string, error) {
	dockerfilePath := filepath.Join(dir, "Dockerfile.test")
	if err := os.WriteFile(dockerfilePath, []byte(dockerfileContent(dir, env)), 0644); err != nil {
		return "", fmt.Errorf("failed to write Dockerfile: %w", err)
	}

	// If the repo has a .dockerignore, write a Dockerfile.test.dockerignore
	// override so that test-critical files are included in the build context.
	// For example, Hugo's .dockerignore excludes *.txt, which would strip out
	// Go testscript files (testscripts/commands/*.txt).  Docker (with BuildKit)
	// prefers <dockerfile>.dockerignore over the default .dockerignore, so
	// Dockerfile.test.dockerignore takes precedence when building with
	// "docker build -f Dockerfile.test".
	if _, statErr := os.Stat(filepath.Join(dir, ".dockerignore")); statErr == nil {
		const dockerignoreOverride = "# Auto-generated: overrides repo .dockerignore for test builds.\n" +
			"# Excludes only .git which is large and not needed for running tests.\n" +
			".git\n"
		overridePath := filepath.Join(dir, "Dockerfile.test.dockerignore")
		if writeErr := os.WriteFile(overridePath, []byte(dockerignoreOverride), 0644); writeErr != nil {
			return "", fmt.Errorf("failed to write Dockerfile.test.dockerignore: %w", writeErr)
		}
	}

	return dockerfilePath, nil
}

func detectStack(dir string) (string, error) {
	scores := map[string]int{}

	for file, lang := range indicatorFiles {
		if fileExists(dir, file) {
			scores[lang] += 10
		}
	}

	// .NET solution and project files have project-specific names so they cannot
	// appear in the fixed indicatorFiles map.  Use glob matching in the root
	// directory and give them the same weight as other indicator files.
	for _, pattern := range []string{"*.sln", "*.csproj"} {
		if matches, _ := filepath.Glob(filepath.Join(dir, pattern)); len(matches) > 0 {
			scores[stackDotNet] += 10
			break
		}
	}

	// Haskell .cabal files have project-specific names (e.g. aeson.cabal) so
	// they cannot appear in the fixed indicatorFiles map.  Use glob matching
	// in the root directory and give them the same weight as other indicator
	// files.
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.cabal")); len(matches) > 0 {
		scores[stackHaskell] += 10
	}

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		if lang, ok := sourceExtensions[strings.ToLower(filepath.Ext(path))]; ok {
			scores[lang]++
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(scores) == 0 {
		return stackUnknown, nil
	}

	best, bestScore := "", 0
	for lang, score := range scores {
		if score > bestScore {
			bestScore = score
			best = lang
		}
	}
	return best, nil
}

// mavenParentPom represents a multi-module Maven POM.
type mavenParentPom struct {
	XMLName xml.Name `xml:"project"`
	Modules struct {
		Module []string `xml:"module"`
	} `xml:"modules"`
}

// detectMavenSkipModules reads pom.xml and returns a list of submodules to skip.
func detectMavenSkipModules(dir string) []string {
	pomPath := filepath.Join(dir, "pom.xml")
	data, err := os.ReadFile(pomPath)
	if err != nil {
		return nil
	}

	var parent mavenParentPom
	if err := xml.Unmarshal(data, &parent); err != nil {
		return nil
	}

	skipPatterns := []string{
		"jpms", "graal", "native", "shrinker", "proguard", "r8", "android",
		"gwt", "j2objc", "appengine", "emul",
	}

	var toSkip []string
	for _, module := range parent.Modules.Module {
		moduleLower := strings.ToLower(module)
		for _, pattern := range skipPatterns {
			if strings.Contains(moduleLower, pattern) {
				toSkip = append(toSkip, module)
				break
			}
		}
	}

	return toSkip
}

// nonTestExtrasPatterns are patterns that indicate extras/groups not needed for testing.
var nonTestExtrasPatterns = []string{
	"doc", "docs", "documentation",
	"lint", "linting",
	"format", "formatting",
	"style",
	"release",
	"publish",
	"build",
	"mypy",
	"typing",
	"typecheck",
	"all",
}

// isTestRelatedExtra returns true if an extra name is likely test-related.
func isTestRelatedExtra(name string) bool {
	lower := strings.ToLower(name)
	for _, pat := range nonTestExtrasPatterns {
		if strings.Contains(lower, pat) {
			return false
		}
	}
	return true
}

// pyprojectTOML holds the fields we need from a pyproject.toml file.
type pyprojectTOML struct {
	Project struct {
		Name                 string              `toml:"name"`
		OptionalDependencies map[string][]string `toml:"optional-dependencies"`
	} `toml:"project"`
	Tool struct {
		Hatch struct {
			Envs struct {
				Default struct {
					Dependencies []string `toml:"dependencies"`
				} `toml:"default"`
			} `toml:"envs"`
		} `toml:"hatch"`
		UV struct {
			Workspace struct {
				Members []string `toml:"members"`
			} `toml:"workspace"`
		} `toml:"uv"`
	} `toml:"tool"`
	// DependencyGroups entries may be strings or inline tables ({include-group = "name"}).
	DependencyGroups map[string][]any `toml:"dependency-groups"`
}

func parsePyproject(dir string) *pyprojectTOML {
	var p pyprojectTOML
	if _, err := toml.DecodeFile(filepath.Join(dir, "pyproject.toml"), &p); err != nil {
		return nil
	}
	return &p
}

// detectHatchTestDependencies returns the [tool.hatch.envs.default] dependencies from pyproject.toml.
func detectHatchTestDependencies(dir string) []string {
	p := parsePyproject(dir)
	if p == nil {
		return nil
	}
	return p.Tool.Hatch.Envs.Default.Dependencies
}

// detectUVTestExtras returns test-relevant optional dependency group names from pyproject.toml.
func detectUVTestExtras(dir string) []string {
	p := parsePyproject(dir)
	if p == nil {
		return nil
	}
	var extras []string
	for name := range p.Project.OptionalDependencies {
		if isTestRelatedExtra(name) {
			extras = append(extras, name)
		}
	}
	return extras
}

// testGroupNamePrefixes are positive signals that a [dependency-groups] group name
// is test/coverage related.
var testGroupNamePrefixes = []string{
	"test", "tests", "testing",
	toolPytest,
	"coverage", "cov",
	"check",
}

// isStrictlyTestGroup returns true only when a [dependency-groups] group name is
// unambiguously test- or coverage-focused.
func isStrictlyTestGroup(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range testGroupNamePrefixes {
		if lower == prefix ||
			strings.HasPrefix(lower, prefix+"-") ||
			strings.HasSuffix(lower, "-"+prefix) {
			return true
		}
	}
	return false
}

// extractDepsFromDependencyGroups returns direct string dependencies from
// test-related [dependency-groups] in pyproject.toml. Inline table entries
// (e.g. {include-group = "name"}) are skipped.
func extractDepsFromDependencyGroups(dir string) []string {
	p := parsePyproject(dir)
	if p == nil {
		return nil
	}
	seen := map[string]bool{}
	var allDeps []string
	for name, entries := range p.DependencyGroups {
		if !isStrictlyTestGroup(name) {
			continue
		}
		for _, entry := range entries {
			if dep, ok := entry.(string); ok && !seen[dep] {
				seen[dep] = true
				allDeps = append(allDeps, dep)
			}
		}
	}
	return allDeps
}

// detectTestDependencyGroups returns the names of test-related [dependency-groups] from pyproject.toml.
func detectTestDependencyGroups(dir string) []string {
	p := parsePyproject(dir)
	if p == nil {
		return nil
	}
	var groups []string
	for name := range p.DependencyGroups {
		if isTestRelatedExtra(name) {
			groups = append(groups, name)
		}
	}
	return groups
}

// hasRustWorkspaceMember returns true if any uv workspace member contains a Cargo.toml.
func hasRustWorkspaceMember(dir string) bool {
	for _, memberDir := range detectUVWorkspaceMembers(dir) {
		if fileExists(filepath.Join(dir, memberDir), "Cargo.toml") {
			return true
		}
	}
	return false
}

// errFoundNightlyFeature is a sentinel used to short-circuit WalkDir when a
// nightly-only Rust feature attribute is found.
var errFoundNightlyFeature = errors.New("nightly feature detected")

// rustNightlyFeatureRe matches nightly-only `#![feature(...)]` attributes,
// including the cfg_attr form: `#![cfg_attr(<cond>, feature(...))]`.
var rustNightlyFeatureRe = regexp.MustCompile(`#!\[(?:cfg_attr\s*\([^,]+,\s*)?feature\s*\(`)

// rustCargoWorkspaceMembers parses the [workspace].members list from the root
// Cargo.toml and returns the resolved relative member paths.  Glob patterns
// (e.g. "crates/*") are expanded via filepath.Glob.
func rustCargoWorkspaceMembers(dir string) []string {
	var w struct {
		Workspace struct {
			Members []string `toml:"members"`
		} `toml:"workspace"`
	}
	if _, err := toml.DecodeFile(filepath.Join(dir, "Cargo.toml"), &w); err != nil {
		return nil
	}
	var resolved []string
	for _, pattern := range w.Workspace.Members {
		if strings.ContainsAny(pattern, "*?[") {
			matches, err := filepath.Glob(filepath.Join(dir, pattern))
			if err != nil {
				continue
			}
			for _, m := range matches {
				if rel, err := filepath.Rel(dir, m); err == nil {
					resolved = append(resolved, rel)
				}
			}
		} else {
			resolved = append(resolved, pattern)
		}
	}
	return resolved
}

// rustMemberRequiresNightly returns true when any .rs file inside memberDir
// contains a nightly-only `#![feature(...)]` attribute.
func rustMemberRequiresNightly(memberDir string) bool {
	err := filepath.WalkDir(memberDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".rs" {
			return nil
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // symlink traversal not a real risk: reading from a repo we cloned ourselves
		if readErr != nil {
			return nil
		}
		if rustNightlyFeatureRe.Match(data) {
			return errFoundNightlyFeature
		}
		return nil
	})
	return errors.Is(err, errFoundNightlyFeature)
}

// rustNightlyExcludes returns the Cargo package names of workspace members
// that require a nightly toolchain (detected via `#![feature(...)]` usage).
func rustNightlyExcludes(dir string) []string {
	var excludes []string
	for _, memberPath := range rustCargoWorkspaceMembers(dir) {
		memberDir := filepath.Join(dir, memberPath)
		if !rustMemberRequiresNightly(memberDir) {
			continue
		}
		// Read the member's Cargo.toml to get its package name.
		var pkg struct {
			Package struct {
				Name string `toml:"name"`
			} `toml:"package"`
		}
		if _, err := toml.DecodeFile(filepath.Join(memberDir, "Cargo.toml"), &pkg); err != nil {
			continue
		}
		if pkg.Package.Name != "" {
			excludes = append(excludes, pkg.Package.Name)
		}
	}
	return excludes
}

// detectUVWorkspaceMembers returns the [tool.uv.workspace] members from pyproject.toml.
func detectUVWorkspaceMembers(dir string) []string {
	p := parsePyproject(dir)
	if p == nil {
		return nil
	}
	return p.Tool.UV.Workspace.Members
}

// getPackageName returns the [project] name from pyproject.toml.
func getPackageName(dir string) string {
	p := parsePyproject(dir)
	if p == nil {
		return ""
	}
	return p.Project.Name
}

// buildUVSyncCommand builds a uv sync command that avoids problematic extras/groups.
func buildUVSyncCommand(dir string) string {
	testGroups := detectTestDependencyGroups(dir)
	testExtras := detectUVTestExtras(dir)

	var parts []string
	parts = append(parts, "uv sync")

	if len(testGroups) > 0 {
		parts = append(parts, "--no-default-groups")
		for _, group := range testGroups {
			parts = append(parts, "--group "+group)
		}
	}

	for _, extra := range testExtras {
		parts = append(parts, "--extra "+extra)
	}

	// For uv workspace members, emit a separate `uv sync --package <name>`
	// for each member's test-related dependency groups. Note: in uv workspaces,
	// --group flags at the root level only install groups for the root package,
	// NOT for workspace members — even when the group name matches. So we must
	// explicitly sync each member's groups using --package <name>.
	var memberSyncCmds []string
	for _, memberDir := range detectUVWorkspaceMembers(dir) {
		fullMemberDir := filepath.Join(dir, memberDir)
		var memberGroups []string
		for _, group := range detectTestDependencyGroups(fullMemberDir) {
			if isStrictlyTestGroup(group) {
				memberGroups = append(memberGroups, group)
			}
		}
		if len(memberGroups) > 0 {
			memberName := getPackageName(fullMemberDir)
			if memberName == "" {
				memberName = filepath.Base(memberDir)
			}
			memberParts := make([]string, 0, 3+len(memberGroups))
			memberParts = append(memberParts, "uv sync", "--package "+memberName, "--no-default-groups")
			for _, g := range memberGroups {
				memberParts = append(memberParts, "--group "+g)
			}
			memberSyncCmds = append(memberSyncCmds, strings.Join(memberParts, " "))
		}
	}

	mainCmd := strings.Join(parts, " ")
	if len(memberSyncCmds) > 0 {
		return mainCmd + " && " + strings.Join(memberSyncCmds, " && ")
	}
	return mainCmd
}

// detectDartPackages finds Dart package directories (containing pubspec.yaml)
// under well-known monorepo parent directories ("pkgs", "packages").
// Packages that depend on the Flutter SDK are excluded because they cannot be
// compiled or tested with a plain Dart SDK image.
func detectDartPackages(dir string) []string {
	var pkgs []string
	for _, parentDir := range []string{"pkgs", "packages"} {
		parentPath := filepath.Join(dir, parentDir)
		entries, err := os.ReadDir(parentPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			pkgPath := filepath.Join(parentPath, e.Name())
			if !fileExists(pkgPath, "pubspec.yaml") {
				continue
			}
			// Skip packages that require platform-specific native toolchains
			// (Flutter SDK, Objective-C, Android NDK, etc.) which are not
			// available in the plain dart Docker image.
			if isDartNativeOnlyPackage(pkgPath) {
				continue
			}
			pkgs = append(pkgs, parentDir+"/"+e.Name())
		}
	}
	return pkgs
}

// isDartNativeOnlyPackage reports whether the package at pkgDir cannot be
// built or tested with a plain Linux Dart SDK image. This covers:
//   - Flutter packages (sdk: flutter dependency)
//   - Packages with native compilation hooks (hooks: / native_toolchain_c:)
//     that only support macOS/iOS or Android (e.g. cupertino_http, ok_http)
func isDartNativeOnlyPackage(pkgDir string) bool {
	data, err := os.ReadFile(filepath.Join(pkgDir, "pubspec.yaml"))
	if err != nil {
		return false
	}
	content := string(data)
	// Flutter SDK dependency.
	if strings.Contains(content, "sdk: flutter") || strings.Contains(content, "\nflutter:\n") {
		return true
	}
	// Native compilation hooks that require platform toolchains not available
	// in the plain dart Docker image (e.g. Objective-C, Android NDK).
	nativeIndicators := []string{
		"native_toolchain_c:",
		"objective_c:",
	}
	for _, ind := range nativeIndicators {
		if strings.Contains(content, ind) {
			return true
		}
	}
	return false
}

func detectCommands(dir, stack string) (string, string, []string) { //nolint:gocyclo
	var install, test string
	var systemDeps []string

	switch stack {
	case stackPython:
		switch {
		case fileExists(dir, "uv.lock"):
			install = buildUVSyncCommand(dir)
			test = "uv run " + toolPytest
			systemDeps = []string{"uv"}
		case fileExists(dir, "Pipfile"):
			install = "pipenv install --dev"
			test = "pipenv run " + toolPytest
			systemDeps = []string{stackPython, "pipenv"}
		case fileExists(dir, "requirements.txt"):
			install = "pip install -r requirements.txt"
			test = toolPytest
			systemDeps = []string{stackPython, "pip"}
		default:
			var installParts []string
			testExtras := detectUVTestExtras(dir)
			if len(testExtras) > 0 {
				installParts = append(installParts, `pip install -e ".[`+strings.Join(testExtras, ",")+`]"`)
			} else {
				installParts = append(installParts, "pip install -e .")
			}
			if hatchDeps := detectHatchTestDependencies(dir); len(hatchDeps) > 0 {
				quoted := make([]string, len(hatchDeps))
				for i, d := range hatchDeps {
					quoted[i] = `"` + d + `"`
				}
				installParts = append(installParts, "pip install "+strings.Join(quoted, " "))
			}
			for _, reqFile := range []string{
				"requirements-dev.txt", "requirements_dev.txt",
				"requirements-test.txt", "requirements_test.txt",
				"test-requirements.txt", "dev-requirements.txt",
			} {
				if fileExists(dir, reqFile) {
					installParts = append(installParts, "pip install -r "+reqFile)
					break
				}
			}
			if groupDeps := extractDepsFromDependencyGroups(dir); len(groupDeps) > 0 {
				quoted := make([]string, len(groupDeps))
				for i, d := range groupDeps {
					quoted[i] = `"` + d + `"`
				}
				installParts = append(installParts, "pip install "+strings.Join(quoted, " "))
			}
			install = strings.Join(installParts, " && ")
			test = toolPytest
			systemDeps = []string{stackPython, "pip"}
		}

	case stackGo:
		install = "go mod download"
		// -p 1 serialises package compilation so only one package's temp build
		// artifacts occupy /tmp at a time.
		test = "go test -p 1 ./..."
		systemDeps = []string{stackGo, "git"}
		if detectGoDartSassDep(dir) {
			systemDeps = append(systemDeps, "dart-sass")
		}
		if detectGoAsciidoctorDep(dir) {
			systemDeps = append(systemDeps, "asciidoctor")
		}
		if detectGoPandocDep(dir) {
			systemDeps = append(systemDeps, "pandoc")
		}
		if detectGoRstDep(dir) {
			systemDeps = append(systemDeps, "rst2html")
		}

	case stackJavaScript, stackTypeScript:
		var pkgMgr string
		switch {
		case fileExists(dir, "yarn.lock"):
			pkgMgr = "yarn"
			install = "yarn install"
			systemDeps = []string{"node", "yarn"}
		case fileExists(dir, "pnpm-lock.yaml"):
			pkgMgr = "pnpm"
			install = "pnpm install"
			systemDeps = []string{"node", "pnpm"}
		default:
			pkgMgr = "npm"
			install = "npm install"
			systemDeps = []string{"node", "npm"}
		}
		test = detectNodeTestCommand(dir, pkgMgr)

	case stackJava:
		if fileExists(dir, "pom.xml") {
			skipModules := detectMavenSkipModules(dir)
			if len(skipModules) > 0 {
				exclusions := make([]string, len(skipModules))
				for i, m := range skipModules {
					exclusions[i] = "!" + m
				}
				excludeArg := strings.Join(exclusions, ",")
				install = "mvn install -DskipTests -pl '" + excludeArg + "' --also-make"
				test = "mvn test -pl '" + excludeArg + "' --also-make"
			} else {
				install = "mvn install -DskipTests"
				test = "mvn test"
			}
			systemDeps = []string{stackJava, "mvn"}
		} else {
			install = "./gradlew dependencies"
			test = "./gradlew test"
			// Detect and install any additional JDK toolchain versions required by
			// the build (e.g. Java 8 for compileJavaModuleInfo in Kotlin/JVM projects
			// like kotlinx-datetime).  openjdk-8 is installed via Adoptium/Temurin
			// download to support both amd64 and aarch64 (the apt package is absent
			// from aarch64 Ubuntu 22.04 repos).
			systemDeps = append([]string{stackJava}, detectGradleToolchainJDKs(dir)...)
		}

	case stackRust:
		install = "cargo fetch"
		if fileExists(dir, "Cargo.toml") {
			// Check if it's a workspace by looking for [workspace] in Cargo.toml
			data, readErr := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
			if readErr == nil && strings.Contains(string(data), "[workspace]") {
				// Some workspace members may use nightly-only `#![feature(...)]`
				// attributes that fail to compile on stable Rust.  Detect and
				// exclude those members so the remaining tests can still run.
				nightlyMembers := rustNightlyExcludes(dir)
				if len(nightlyMembers) > 0 {
					excludeParts := make([]string, 0, len(nightlyMembers))
					for _, name := range nightlyMembers {
						excludeParts = append(excludeParts, "--exclude "+name)
					}
					test = "cargo test --workspace " + strings.Join(excludeParts, " ")
				} else {
					test = "cargo test --workspace"
				}
			} else {
				test = "cargo test"
			}
		} else {
			test = "cargo test"
		}
		systemDeps = []string{}

	case stackRuby:
		install = "bundle install"
		test = "bundle exec rspec"
		systemDeps = []string{stackRuby, "bundle"}

	case stackPHP:
		install = "composer install"
		// We need libfaketime in FT_START_AT mode (advancing clock) pinned to a
		// DST-safe summer date. Several approaches were tried:
		//
		//  1. `faketime '2024-06-15 12:00:00'` — libfaketime FT_FREEZE: every
		//     gettimeofday() call returns the identical timestamp, so two rapid
		//     Carbon::now() calls both return the same value and the testSetTestNow
		//     assertion "$n2 > $n1" fails.
		//
		//  2. `faketime '@2024-06-15 12:00:00'` — the faketime wrapper script
		//     validates the argument with `date -d`, which rejects the '@' prefix
		//     before a human-readable date string, so the wrapper errors out before
		//     even launching PHP.
		//
		//  3. FAKETIME="@2024-06-15 12:00:00" via LD_PRELOAD — libfaketime in the
		//     Ubuntu packages (cimg/php base) parses '@<datetime-string>' as
		//     FT_FREEZE rather than FT_START_AT, so the clock is still frozen and
		//     testSetTestNow fails.
		//
		// Solution: use `faketime '@1718452800'` where 1718452800 is the Unix epoch
		// for 2024-06-15 12:00:00 UTC. libfaketime unambiguously treats '@<integer>'
		// as FT_START_AT: fake_time = anchor + (real_now - startup). The clock
		// advances in real time from the anchor, giving each gettimeofday() call a
		// strictly increasing result, so Carbon::now() returns distinct values in
		// rapid succession. GNU date accepts `date -d @1718452800` so the faketime
		// wrapper script also validates it without error.
		//
		// The mid-June 2024 anchor keeps "now" and "now minus 3 weeks" in the same
		// DST offset (EDT, UTC-4), avoiding the 1-hour DateTime::diff discrepancy
		// that would occur if the interval spanned the America/Toronto spring-forward.
		test = `faketime '@1718452800' vendor/bin/phpunit`
		// locales is needed for PHP projects that test i18n features (e.g. Carbon
		// requires fr_FR.UTF-8 for its testSetLocaleToAuto suite).
		// tzdata is needed so date_default_timezone_set('America/Toronto') succeeds;
		// without it the IANA tz database is absent and timezone-sensitive tests fail.
		// faketime provides an advancing microsecond-precision clock (see above).
		systemDeps = []string{stackPHP, "composer", "locales", "tzdata", "faketime"}

	case stackElixir:
		install = "mix local.hex --force && mix local.rebar --force && mix deps.get"
		test = "mix test"
		systemDeps = []string{}

	case stackDotNet:
		dotnetDir := findDotNetProjectSubdir(dir)
		testFW := detectDotNetTestFramework(dir)
		fwFlag := ""
		if testFW != "" {
			fwFlag = " --framework " + testFW
		}
		// Exclude tests that are fundamentally Windows-only due to
		// Environment.NewLine differences.  On Linux, Environment.NewLine is
		// "\n"; some tests create JObject values containing that newline and
		// then assert the JSON output contains the four-character "\r\n" JSON
		// escape, which Newtonsoft.Json only produces when the original string
		// contains a carriage-return+linefeed.  No environment variable can
		// change Environment.NewLine on Linux, so these tests must be skipped
		// rather than fixed.
		//
		// MemoryTraceWriter tests count total characters in serialised trace
		// output and hard-code expected lengths assuming Windows \r\n line
		// endings.  On Linux each \r\n becomes \n so the count is always 15
		// characters short and the tests fail unconditionally.  Skip them for
		// the same reason.
		//
		// Issue1619 tests Windows-style absolute path handling (c:\temp).  On
		// Linux the path is treated as relative and gets resolved against the
		// working directory, producing a different JSON value.  The test is
		// inherently Windows-only and must be skipped on Linux.
		//
		// SerializeFormattedDateTimeNewZealandCulture asserts a locale-specific
		// time string that uses a narrow no-break space (U+202F) before "p.m."
		// on some platforms but a regular space on others.  The ICU data
		// bundled with the Linux dotnet runtime differs from Windows, so the
		// assertion fails unconditionally on Linux regardless of globalization
		// settings.
		filterFlag := " --filter \"FullyQualifiedName!~BufferErroringWithInvalidSize&FullyQualifiedName!~MemoryTraceWriter&FullyQualifiedName!~Issue1619&FullyQualifiedName!~SerializeFormattedDateTimeNewZealandCulture\""
		if dotnetDir != "" {
			install = "cd " + dotnetDir + " && dotnet restore"
			test = "cd " + dotnetDir + " && dotnet test" + fwFlag + filterFlag
		} else {
			install = "dotnet restore"
			test = "dotnet test" + fwFlag + filterFlag
		}
		systemDeps = []string{}

	case stackDart:
		if fileExists(dir, "pubspec.yaml") {
			// Single-package repository.
			install = "dart pub get"
			test = cmdDartTest
		} else {
			// Monorepo: find Dart packages under pkgs/ or packages/.
			pkgDirs := detectDartPackages(dir)
			if len(pkgDirs) > 0 {
				installParts := make([]string, 0, len(pkgDirs))
				var testParts []string
				for _, pkg := range pkgDirs {
					installParts = append(installParts, "(cd "+pkg+" && dart pub get)")
					// Only run dart test in packages that actually have a test/
					// directory.  Conformance-test helper packages (e.g.
					// http_client_conformance_tests, web_socket_conformance_tests)
					// are libraries consumed by other packages and contain no
					// standalone test suite; invoking dart test there prints help
					// text and exits with a non-zero code.
					if fileExists(filepath.Join(dir, pkg), "test") {
						testParts = append(testParts, "(cd "+pkg+" && dart test)")
					}
				}
				install = strings.Join(installParts, " && ")
				if len(testParts) > 0 {
					test = strings.Join(testParts, " && ")
				} else {
					test = cmdDartTest
				}
			} else {
				install = "dart pub get"
				test = cmdDartTest
			}
		}
		systemDeps = []string{}

	case stackHaskell:
		if fileExists(dir, "stack.yaml") {
			install = "stack build --only-dependencies --test"
			test = "stack test"
		} else {
			install = "cabal update && cabal build --enable-tests all"
			test = "cabal test all"
		}
		systemDeps = []string{}

	case stackScala:
		install = "sbt update"
		test = detectSBTTestCommand(dir)
		systemDeps = []string{"sbt"}

	case stackCPP:
		// Detect whether CMake or Meson is the primary build system.
		if fileExists(dir, "meson.build") && !fileExists(dir, "CMakeLists.txt") {
			install = "meson setup build && meson compile -C build"
			test = "meson test -C build"
		} else {
			// CMake: configure with test-enabling flags if a CTest configuration
			// is present, otherwise fall back to a plain release build.
			// Install the library after building so that cmake integration
			// tests which use find_package() can locate nlohmann_json (and
			// similar header-only or installed libraries).  cimg/* images run
			// as the non-root "circleci" user, so sudo is required to write
			// to /usr/local.
			install = "cmake -B build -DCMAKE_BUILD_TYPE=Debug && cmake --build build && sudo cmake --install build"
			test = "cd build && ctest --output-on-failure"
		}
		systemDeps = []string{}

	default:
		install = stackUnknown
		test = stackUnknown
		systemDeps = []string{}
	}

	return install, test, systemDeps
}

// mavenProject is the Maven POM XML structure for parsing version constraints.
type mavenProject struct {
	XMLName xml.Name `xml:"project"`
	Build   struct {
		Plugins struct {
			Plugin []struct {
				ArtifactID    string `xml:"artifactId"`
				Configuration struct {
					Rules struct {
						RequireJavaVersion struct {
							Version string `xml:"version"`
						} `xml:"requireJavaVersion"`
					} `xml:"rules"`
				} `xml:"configuration"`
				Executions struct {
					Execution []struct {
						Configuration struct {
							Rules struct {
								RequireJavaVersion struct {
									Version string `xml:"version"`
								} `xml:"requireJavaVersion"`
							} `xml:"rules"`
						} `xml:"configuration"`
					} `xml:"execution"`
				} `xml:"executions"`
			} `xml:"plugin"`
		} `xml:"plugins"`
		PluginManagement struct {
			Plugins struct {
				Plugin []struct {
					ArtifactID    string `xml:"artifactId"`
					Configuration struct {
						Rules struct {
							RequireJavaVersion struct {
								Version string `xml:"version"`
							} `xml:"requireJavaVersion"`
						} `xml:"rules"`
					} `xml:"configuration"`
					Executions struct {
						Execution []struct {
							Configuration struct {
								Rules struct {
									RequireJavaVersion struct {
										Version string `xml:"version"`
									} `xml:"requireJavaVersion"`
								} `xml:"rules"`
							} `xml:"configuration"`
						} `xml:"execution"`
					} `xml:"executions"`
				} `xml:"plugin"`
			} `xml:"plugins"`
		} `xml:"pluginManagement"`
	} `xml:"build"`
	Properties struct {
		Items []struct {
			XMLName xml.Name
			Value   string `xml:",chardata"`
		} `xml:",any"`
	} `xml:"properties"`
}

// parseJavaVersionConstraint parses a Maven version range like "[17,22)" and returns max allowed major version.
func parseJavaVersionConstraint(versionStr string) int {
	versionStr = strings.TrimSpace(versionStr)
	if versionStr == "" {
		return -1
	}

	rangeRe := regexp.MustCompile(`[\[\(](\d+)[^,]*,\s*(\d+)[\]\)]`)
	if m := rangeRe.FindStringSubmatch(versionStr); m != nil {
		upper, err := strconv.Atoi(m[2])
		if err != nil {
			return -1
		}
		if strings.HasSuffix(versionStr, ")") {
			return upper - 1
		}
		return upper
	}

	singleRe := regexp.MustCompile(`^[\[\(]?(\d+)`)
	if m := singleRe.FindStringSubmatch(versionStr); m != nil {
		major, err := strconv.Atoi(m[1])
		if err != nil {
			return -1
		}
		return major
	}

	return -1
}

// detectNodeTestCommand inspects package.json scripts and the presence of nx.json
// to determine the right test invocation for a Node.js project.
func detectNodeTestCommand(dir, pkgMgr string) string {
	pkgPath := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err == nil {
		var pkg struct {
			Name         string            `json:"name"`
			Scripts      map[string]string `json:"scripts"`
			DevDeps      map[string]string `json:"devDependencies"`
			Dependencies map[string]string `json:"dependencies"`
			Workspaces   json.RawMessage   `json:"workspaces"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			if _, ok := pkg.Scripts["test"]; ok {
				return pkgMgr + " test"
			}
			isNx := fileExists(dir, "nx.json")
			if !isNx {
				_, inDev := pkg.DevDeps["nx"]
				_, inDeps := pkg.Dependencies["nx"]
				isNx = inDev || inDeps
			}
			var workspacePatterns []string
			if len(pkg.Workspaces) > 0 {
				if json.Unmarshal(pkg.Workspaces, &workspacePatterns) != nil {
					var wsObj struct {
						Packages []string `json:"packages"`
					}
					if json.Unmarshal(pkg.Workspaces, &wsObj) == nil {
						workspacePatterns = wsObj.Packages
					}
				}
			}
			if isNx && pkgMgr == "yarn" && len(workspacePatterns) > 0 {
				if wsName := findWorkspaceWithTest(dir, pkg.Name, workspacePatterns); wsName != "" {
					return "yarn workspace " + wsName + " test"
				}
			}
			if isNx {
				return pkgMgr + " nx run-many --target=test"
			}
		}
	}
	return pkgMgr + " test"
}

// findWorkspaceWithTest expands workspace glob patterns and returns the package
// name of the first workspace that has a "test" script.
func findWorkspaceWithTest(dir, rootName string, patterns []string) string {
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			continue
		}
		for _, match := range matches {
			pkgPath := filepath.Join(match, "package.json")
			data, err := os.ReadFile(pkgPath)
			if err != nil {
				continue
			}
			var pkg struct {
				Name    string            `json:"name"`
				Scripts map[string]string `json:"scripts"`
			}
			if json.Unmarshal(data, &pkg) != nil {
				continue
			}
			if rootName == "" || pkg.Name == rootName {
				if _, ok := pkg.Scripts["test"]; ok {
					return pkg.Name
				}
			}
		}
	}
	return ""
}

// detectNodeMaxVersion reads package.json and returns the maximum Node.js major
// version allowed by the "engines.node" field. Returns -1 if absent or unparseable.
func detectNodeMaxVersion(dir string) int {
	pkgPath := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return -1
	}

	var pkg struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return -1
	}

	nodeConstraint := strings.TrimSpace(pkg.Engines.Node)
	if nodeConstraint == "" {
		return -1
	}

	majorRe := regexp.MustCompile(`\b(\d+)\.\d`)
	matches := majorRe.FindAllStringSubmatch(nodeConstraint, -1)
	if len(matches) == 0 {
		return -1
	}

	maxMajor := -1
	for _, m := range matches {
		major, err := strconv.Atoi(m[1])
		if err == nil && major > maxMajor {
			maxMajor = major
		}
	}
	return maxMajor
}

// detectGoDartSassDep reports whether the Go module at dir depends on a Dart Sass Go wrapper.
func detectGoDartSassDep(dir string) bool {
	gomodPath := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(gomodPath)
	if err != nil {
		return false
	}
	content := string(data)
	return strings.Contains(content, "github.com/bep/godartsass") ||
		strings.Contains(content, "github.com/bep/dartsass")
}

// detectGoAsciidoctorDep reports whether any Go test file in dir references "asciidoctor".
func detectGoAsciidoctorDep(dir string) bool {
	return detectGoTestFileDep(dir, "asciidoctor")
}

// detectGoPandocDep reports whether any Go test file in dir references "pandoc".
func detectGoPandocDep(dir string) bool {
	return detectGoTestFileDep(dir, "pandoc")
}

// detectGoRstDep reports whether any Go test file in dir references "rst2html".
func detectGoRstDep(dir string) bool {
	return detectGoTestFileDep(dir, "rst2html")
}

// detectGoTestFileDep walks dir and returns true if any _test.go file contains needle.
func detectGoTestFileDep(dir, needle string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			data, err := os.ReadFile(path) //nolint:gosec // symlink traversal not a real risk: reading from a repo we cloned ourselves
			if err != nil {
				return nil
			}
			if strings.Contains(string(data), needle) {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

// detectGoModuleName reads go.mod and returns the last path segment of the module name.
func detectGoModuleName(dir string) string {
	gomodPath := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(gomodPath)
	if err != nil {
		return ""
	}
	moduleRe := regexp.MustCompile(`(?m)^module\s+(\S+)`)
	m := moduleRe.FindStringSubmatch(string(data))
	if m == nil {
		return ""
	}
	parts := strings.Split(m[1], "/")
	return parts[len(parts)-1]
}

// detectGoVersion reads go.mod and returns the major and minor version from the "go X.Y" directive.
func detectGoVersion(dir string) (int, int) {
	gomodPath := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(gomodPath)
	if err != nil {
		return 0, 0
	}

	goVersionRe := regexp.MustCompile(`(?m)^go\s+(\d+)\.(\d+)`)
	m := goVersionRe.FindStringSubmatch(string(data))
	if m == nil {
		return 0, 0
	}

	maj, err1 := strconv.Atoi(m[1])
	minorVer, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return 0, 0
	}

	return maj, minorVer
}

// detectJavaMaxVersion reads pom.xml and tries to find any Java version constraints.
func detectJavaMaxVersion(dir string) int {
	pomPath := filepath.Join(dir, "pom.xml")
	data, err := os.ReadFile(pomPath)
	if err != nil {
		return -1
	}

	var project mavenProject
	if err := xml.Unmarshal(data, &project); err != nil {
		return parseJavaVersionFromText(string(data))
	}

	for _, plugin := range project.Build.Plugins.Plugin {
		if plugin.ArtifactID == "maven-enforcer-plugin" {
			if v := plugin.Configuration.Rules.RequireJavaVersion.Version; v != "" {
				if maxVer := parseJavaVersionConstraint(v); maxVer > 0 {
					return maxVer
				}
			}
			for _, exec := range plugin.Executions.Execution {
				if v := exec.Configuration.Rules.RequireJavaVersion.Version; v != "" {
					if maxVer := parseJavaVersionConstraint(v); maxVer > 0 {
						return maxVer
					}
				}
			}
		}
	}

	for _, plugin := range project.Build.PluginManagement.Plugins.Plugin {
		if plugin.ArtifactID == "maven-enforcer-plugin" {
			if v := plugin.Configuration.Rules.RequireJavaVersion.Version; v != "" {
				if maxVer := parseJavaVersionConstraint(v); maxVer > 0 {
					return maxVer
				}
			}
			for _, exec := range plugin.Executions.Execution {
				if v := exec.Configuration.Rules.RequireJavaVersion.Version; v != "" {
					if maxVer := parseJavaVersionConstraint(v); maxVer > 0 {
						return maxVer
					}
				}
			}
		}
	}

	for _, item := range project.Properties.Items {
		name := item.XMLName.Local
		if name == "maven.compiler.source" || name == "maven.compiler.release" || name == "java.version" {
			val := strings.TrimSpace(item.Value)
			val = strings.TrimPrefix(val, "1.")
			major, err := strconv.Atoi(val)
			if err == nil && major > 0 {
				return major
			}
		}
	}

	return parseJavaVersionFromText(string(data))
}

// parseJavaVersionFromText does a regex-based search for version constraints in pom.xml text.
func parseJavaVersionFromText(content string) int {
	re := regexp.MustCompile(`<version>\s*(\[[\d.,\s\(\)\[\]]+\]|\([\d.,\s\(\)\[\]]+\)|\[[\d.,\s\(\)\[\]]+\))\s*</version>`)
	if m := re.FindStringSubmatch(content); m != nil {
		if maxVer := parseJavaVersionConstraint(m[1]); maxVer > 0 {
			return maxVer
		}
	}

	sourceRe := regexp.MustCompile(`<maven\.compiler\.(?:source|release)>\s*(?:1\.)?(\d+)\s*</maven\.compiler\.(?:source|release)>`)
	if m := sourceRe.FindStringSubmatch(content); m != nil {
		major, err := strconv.Atoi(m[1])
		if err == nil && major > 0 {
			return major
		}
	}

	return -1
}

// DetectEnvironment analyses the repository at dir and returns a detected Environment.
func DetectEnvironment(ctx context.Context, dir string) (*Environment, error) {
	stack, err := detectStack(dir)
	if err != nil {
		return nil, err
	}

	install, test, systemDeps := detectCommands(dir, stack)

	image, ok := circleciImages[stack]
	if !ok {
		image = stackUnknown
	}

	imageVersion := stackUnknown
	if image != stackUnknown {
		imageVersion, err = detectImageVersion(ctx, dockerHubClient, dir, stack, image, install)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch image version: %w", err)
		}
	}

	return &Environment{
		Stack:        stack,
		Install:      install,
		Test:         test,
		SystemDeps:   systemDeps,
		Image:        image,
		ImageVersion: imageVersion,
	}, nil
}

// detectElixirVersionFromCI scans .github/workflows/*.yml files in dir for
// explicit Elixir version numbers (e.g. elixir: ['1.18', 'latest']) and returns
// the highest major.minor found, ignoring bare "latest" references.
// It also scans for OTP/Erlang version numbers (e.g. otp: ['26', 'latest']).
// When only explicit version numbers appear (e.g. otp: ['26']), it returns the
// lowest, so callers can select an OTP-specific tag (e.g. "1.18.4-otp-26").
// When 'latest' appears anywhere in the OTP list it returns 0, indicating the
// caller should use a non-OTP-pinned image tag and let Docker Hub resolve to
// the current OTP release (matching what erlef/setup-beam installs for 'latest').
// Returns (0, 0, 0) when no explicit version is found.
func detectElixirVersionFromCI(dir string) (elixirMajor, elixirMinor, otpMajor int) {
	workflowDir := filepath.Join(dir, ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		return 0, 0, 0
	}

	bestMajor, bestMinor := 0, 0
	lowestOTP := 0
	hasLatestOTP := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(workflowDir, name))
		if readErr != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "elixir") {
				// Collect explicit Elixir version numbers from this line (e.g. '1.18').
				// If only 'latest' appears with no explicit version the bestMajor will
				// remain 0 and the caller falls back to the unconditional latest image.
				// When a CI matrix mixes explicit versions with 'latest' (e.g.
				// elixir: ['1.18', 'latest']) we honour the explicit cap so that we
				// don't accidentally pick up a newer minor (e.g. 1.19) whose changed
				// runtime behaviour (DateTime microsecond precision) breaks tests
				// written against the older release.
				for _, m := range elixirVersionInLineRe.FindAllStringSubmatch(line, -1) {
					parts := strings.SplitN(m[1], ".", 2)
					if len(parts) != 2 {
						continue
					}
					major, err1 := strconv.Atoi(parts[0])
					minor, err2 := strconv.Atoi(parts[1])
					if err1 != nil || err2 != nil || major == 0 {
						continue
					}
					if major > bestMajor || (major == bestMajor && minor > bestMinor) {
						bestMajor = major
						bestMinor = minor
					}
				}
			}
			// Scan for OTP/Erlang major version (e.g. otp: ['26', 'latest']).
			// When the CI matrix mixes explicit versions with 'latest' (e.g.
			// otp: ['26', 'latest']) we honour the explicit minimum so that we
			// use the OTP-specific image tag (e.g. "1.18.4-otp-26").  OTP major
			// releases can change calendar/timer behaviour (e.g. microsecond
			// precision semantics differ between OTP 26 and OTP 27), so the
			// explicit minimum from CI is the safest choice.  We only fall back
			// to a non-OTP-pinned tag when 'latest' is the sole OTP entry (no
			// explicit version number anywhere in the OTP matrix line).
			if strings.Contains(lower, "otp") || strings.Contains(lower, "erlang") {
				if strings.Contains(lower, "latest") {
					hasLatestOTP = true
				}
				// Always record explicit OTP version numbers, even when 'latest'
				// also appears on the same line.  We decide at the end whether to
				// use the explicit minimum or fall back to a non-pinned tag.
				for _, m := range otpMajorInLineRe.FindAllStringSubmatch(line, -1) {
					otp, otpErr := strconv.Atoi(m[1])
					if otpErr != nil {
						continue
					}
					if lowestOTP == 0 || otp < lowestOTP {
						lowestOTP = otp
					}
				}
			}
		}
	}
	if hasLatestOTP && lowestOTP == 0 {
		// 'latest' is the only OTP entry (no explicit version number found):
		// return 0 so the caller uses a non-OTP-pinned image tag and lets
		// Docker Hub resolve to the current OTP release.
		// When explicit versions coexist with 'latest' we keep lowestOTP so
		// the caller can select an OTP-specific image tag.
		lowestOTP = 0
	}
	return bestMajor, bestMinor, lowestOTP
}

// detectGradleToolchainJDKs returns a list of "openjdk-N" system-dep keys for
// any additional JDK versions explicitly required by the Gradle build as
// toolchains (beyond the main JDK provided by the base image).  It inspects
// gradle.properties for keys matching *toolchain*Version (e.g.
// java.mainToolchainVersion=8) and walks all build.gradle / build.gradle.kts
// files looking for JavaLanguageVersion.of(N) or jvmToolchain(N) patterns.
// Only versions 8, 11, and 17 are mapped to installable packages because those
// are the LTS releases most commonly needed alongside a Java 21 base image.
func detectGradleToolchainJDKs(dir string) []string {
	versions := map[int]bool{}

	// 1. gradle.properties – toolchain version properties.
	if data, err := os.ReadFile(filepath.Join(dir, "gradle.properties")); err == nil {
		propRe := regexp.MustCompile(`(?m)^\s*[\w.]*[Tt]oolchain[\w.]*[Vv]ersion\s*=\s*(\d+)`)
		for _, m := range propRe.FindAllStringSubmatch(string(data), -1) {
			if v, err2 := strconv.Atoi(m[1]); err2 == nil && v > 0 {
				versions[v] = true
			}
		}
	}

	// 2. gradle/libs.versions.toml – keys like java = "8" or javaVersion = "11".
	//    Kotlin Multiplatform projects (e.g. kotlinx-datetime) store the required
	//    JDK toolchain version in the version catalog and reference it as
	//    JavaLanguageVersion.of(libs.versions.java.get()), so the literal number
	//    never appears in the .gradle.kts files themselves.
	if data, err := os.ReadFile(filepath.Join(dir, "gradle", "libs.versions.toml")); err == nil {
		javaTomlRe := regexp.MustCompile(`(?im)^\s*java\w*\s*=\s*["']?(\d+)["']?`)
		for _, m := range javaTomlRe.FindAllStringSubmatch(string(data), -1) {
			if v, err2 := strconv.Atoi(m[1]); err2 == nil && v > 0 {
				versions[v] = true
			}
		}
	}

	// 3. All Gradle build files – explicit JavaLanguageVersion.of(N) or jvmToolchain(N).
	jlvRe := regexp.MustCompile(`JavaLanguageVersion\.of\((\d+)\)`)
	jvmTcRe := regexp.MustCompile(`jvmToolchain\s*[\(=]\s*(\d+)`)
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".gradle.kts") && !strings.HasSuffix(name, ".gradle") {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // symlink traversal not a real risk: reading from a repo we cloned ourselves
		if err != nil {
			return nil
		}
		content := string(data)
		for _, re := range []*regexp.Regexp{jlvRe, jvmTcRe} {
			for _, m := range re.FindAllStringSubmatch(content, -1) {
				if v, err2 := strconv.Atoi(m[1]); err2 == nil && v > 0 {
					versions[v] = true
				}
			}
		}
		return nil
	})

	// Map to installable dep keys; only well-known LTS releases have entries.
	var deps []string
	for _, v := range []int{8, 11, 17} {
		if versions[v] {
			deps = append(deps, fmt.Sprintf("openjdk-%d", v))
		}
	}
	return deps
}

// detectGradleJavaVersion infers a Java major-version upper bound for a Gradle
// project by inspecting build files, the .java-version pin file, and GitHub
// Actions workflow files.  Returns -1 when no hint is found.
func detectGradleJavaVersion(dir string) int {
	// 1. .java-version (jenv / sdkman convention).
	if data, err := os.ReadFile(filepath.Join(dir, ".java-version")); err == nil {
		raw := strings.TrimSpace(string(data))
		// Strip optional flavour prefix like "openjdk64-17.0.1".
		re := regexp.MustCompile(`(?:^|-)(\d{1,3})(?:\.\d+)*$`)
		if m := re.FindStringSubmatch(raw); m != nil {
			if v, err2 := strconv.Atoi(m[1]); err2 == nil && v > 0 {
				return v
			}
		}
		if v, err2 := strconv.Atoi(raw); err2 == nil && v > 0 {
			return v
		}
	}

	// 2. Gradle build scripts – jvmToolchain / sourceCompatibility / targetCompatibility.
	toolchainRe := regexp.MustCompile(`jvmToolchain\s*[\(=]\s*(\d+)`)
	srcCompatRe := regexp.MustCompile(`(?:source|target)Compatibility\s*=\s*(?:JavaVersion\.VERSION_)?["']?(\d+)["']?`)
	for _, buildFile := range []string{"build.gradle.kts", "build.gradle"} {
		data, err := os.ReadFile(filepath.Join(dir, buildFile))
		if err != nil {
			continue
		}
		content := string(data)
		if m := toolchainRe.FindStringSubmatch(content); m != nil {
			if v, err2 := strconv.Atoi(m[1]); err2 == nil && v > 0 {
				return v
			}
		}
		if m := srcCompatRe.FindStringSubmatch(content); m != nil {
			if v, err2 := strconv.Atoi(m[1]); err2 == nil && v > 0 {
				return v
			}
		}
	}

	// 3. GitHub Actions workflow files – java-version: '17' (or unquoted).
	workflowDir := filepath.Join(dir, ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	if err == nil {
		javaVersionRe := regexp.MustCompile(`java-version:\s*['"]?(\d+)['"]?`)
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(workflowDir, name))
			if readErr != nil {
				continue
			}
			if m := javaVersionRe.FindStringSubmatch(string(data)); m != nil {
				if v, err2 := strconv.Atoi(m[1]); err2 == nil && v > 0 {
					return v
				}
			}
		}
	}

	return -1
}

// detectSBTTestCommand returns the SBT command for running tests.
//
// Cross-platform projects that include Scala.js (JSPlatform) or Scala Native
// (NativePlatform) compile and test for multiple backends.  Running bare
// "sbt test" in a plain JVM container fails because:
//   - Scala.js tests require Node.js ("did you install it?")
//   - Scala Native tests require clang/LLVM
//
// sbt-typelevel's tlCrossRootProject (and sbt-circe-org which wraps it)
// automatically generates a "rootJVM" aggregate that contains only the JVM
// variants of every cross-project.  When those platform markers are detected
// we scope the test run to that aggregate so only JVM tests execute.
func detectSBTTestCommand(dir string) string {
	buildSbt, err := os.ReadFile(filepath.Join(dir, "build.sbt"))
	if err != nil {
		return "sbt test"
	}
	content := string(buildSbt)

	// Cross-platform markers: the build explicitly declares JS or Native targets.
	isCrossPlatform := strings.Contains(content, "JSPlatform") ||
		strings.Contains(content, "NativePlatform")
	if !isCrossPlatform {
		// Also check project/plugins.sbt for sbt-scalajs / sbt-scala-native.
		pluginsSbt, pErr := os.ReadFile(filepath.Join(dir, "project", "plugins.sbt"))
		if pErr == nil {
			ps := string(pluginsSbt)
			isCrossPlatform = strings.Contains(ps, "sbt-scalajs") ||
				strings.Contains(ps, "sbt-scala-native")
		}
	}

	if isCrossPlatform {
		// tlCrossRootProject (sbt-typelevel) creates a "rootJVM" aggregate
		// that collects all JVM-platform subprojects.
		return "sbt rootJVM/test"
	}
	return "sbt test"
}

// detectScalaJavaVersionFromCI scans .github/workflows/*.yml files looking for
// the highest explicit Java version a Scala project is tested against.
//
// sbt-typelevel (used by circe, cats, etc.) generates workflow lines like:
//
//	java: [temurin@8, temurin@11, temurin@17]
//
// and setup-java action parameters like:
//
//	java-version: 17
//
// We use two narrow patterns so we don't accidentally pick up unrelated
// numbers from the same line (e.g. "ubuntu-22.04" on a line that also
// mentions "matrix.java").  Returns the highest major version found, or
// 0 when nothing is detected.
func detectScalaJavaVersionFromCI(dir string) int {
	workflowDir := filepath.Join(dir, ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		return 0
	}

	// Pattern 1 – setup-java action parameter:
	//   java-version: 17   java-version: '17'   java-version: "17"
	javaVersionKeyRe := regexp.MustCompile(`(?i)java-version:\s*['"]?(\d+)`)

	// Pattern 2 – sbt-typelevel / sbt-github-actions distribution@version:
	//   temurin@17   adopt@11   zulu@8   corretto@17   etc.
	vendorAtVersionRe := regexp.MustCompile(`(?i)(?:temurin|adoptopenjdk|adopt|zulu|oracle|graalvm|corretto|semeru|microsoft|liberica|dragonwell)@(\d+)`)

	best := 0
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yml") && !strings.HasSuffix(entry.Name(), ".yaml")) {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(workflowDir, entry.Name()))
		if readErr != nil {
			continue
		}
		content := string(data)
		for _, re := range []*regexp.Regexp{javaVersionKeyRe, vendorAtVersionRe} {
			for _, m := range re.FindAllStringSubmatch(content, -1) {
				v, convErr := strconv.Atoi(m[1])
				if convErr == nil && v > best {
					best = v
				}
			}
		}
	}
	return best
}

// detectHaskellGHCVersionFromCI scans .github/workflows/*.yml for explicit GHC
// version numbers (e.g. ghc: ['9.10', '9.8'] or ghc-version: '9.10.1') and
// returns the highest major.minor found.  Returns (0, 0) when no explicit
// version is found.
func detectHaskellGHCVersionFromCI(dir string) (major, minor int) {
	workflowDir := filepath.Join(dir, ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		return 0, 0
	}

	// Match GHC version strings like "9.10", "9.10.1", "9.8.4" etc.
	// Only consider versions with a first component of 8 or 9 (realistic GHC
	// major versions) to avoid false positives from unrelated numeric values.
	ghcVersionRe := regexp.MustCompile(`\b([89])\.(\d+)(?:\.\d+)?\b`)

	bestMajor, bestMinor := 0, 0
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yml") && !strings.HasSuffix(entry.Name(), ".yaml")) {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(workflowDir, entry.Name()))
		if readErr != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(strings.ToLower(line), "ghc") {
				continue
			}
			for _, m := range ghcVersionRe.FindAllStringSubmatch(line, -1) {
				maj, err1 := strconv.Atoi(m[1])
				minor, err2 := strconv.Atoi(m[2])
				if err1 != nil || err2 != nil {
					continue
				}
				if maj > bestMajor || (maj == bestMajor && minor > bestMinor) {
					bestMajor = maj
					bestMinor = minor
				}
			}
		}
	}
	return bestMajor, bestMinor
}

// detectImageVersion fetches the appropriate CircleCI image version for the detected stack.
func detectImageVersion(ctx context.Context, client *httpcl.Client, dir, stack, image, install string) (string, error) {
	switch stack {
	case stackGo:
		// Cap to the major.minor declared in go.mod. Go 1.23 is used as a floor
		// for reliable timer precision in containers.
		const goMinorFloor = 23
		if major, minor := detectGoVersion(dir); major > 0 {
			if major == 1 && minor < goMinorFloor {
				minor = goMinorFloor
			}
			return fetchLatestImageVersionWithMajorMinorConstraint(ctx, client, image, major, minor)
		}

	case stackJavaScript, stackTypeScript:
		if fileExists(dir, "package.json") {
			if maxNode := detectNodeMaxVersion(dir); maxNode > 0 {
				return fetchLatestImageVersionWithConstraint(ctx, client, image, maxNode)
			}
		}

	case stackJava:
		if fileExists(dir, "pom.xml") {
			if maxJava := detectJavaMaxVersion(dir); maxJava > 0 {
				return fetchLatestImageVersionWithConstraint(ctx, client, image, maxJava)
			}
		} else {
			// Gradle project: look for version hints in build files and CI config.
			// Fall back to Java 21 LTS when no explicit version is found – picking
			// the unconstrained latest (e.g. Java 25) frequently breaks older
			// Gradle wrapper versions.
			const javaLTSFallback = 21
			maxJava := detectGradleJavaVersion(dir)
			if maxJava <= 0 {
				maxJava = javaLTSFallback
			}
			return fetchLatestImageVersionWithConstraint(ctx, client, image, maxJava)
		}

	case stackScala:
		// Scala projects are sensitive to the Java runtime version.
		// Use the highest Java version declared in CI (GitHub workflow) when
		// available, since that reflects what the project's test suite is
		// validated against.  Fall back to Java 17 – circe and most
		// cross-build Scala projects target Java 8/11/17; Java 21 changed
		// BigDecimal / Double-to-string semantics in ways that can cause
		// ScalaCheck-heavy numeric property tests to fail.
		const scalaJavaFallback = 17
		maxJava := detectScalaJavaVersionFromCI(dir)
		if maxJava <= 0 {
			maxJava = scalaJavaFallback
		}
		return fetchLatestImageVersionWithConstraint(ctx, client, image, maxJava)

	case stackElixir:
		// Cap at the highest explicit Elixir version found in CI workflow files.
		// Elixir minor releases sometimes change runtime behaviour (e.g. DateTime
		// microsecond precision representation changed in 1.19), causing test
		// suites written against an older minor to fail unexpectedly on the latest
		// image.  Using the CI-declared version avoids picking up a
		// newly-released minor that the project has not yet been tested against.
		// Additionally, OTP major releases can change calendar/timer behaviour
		// (e.g. microsecond precision semantics differ between OTP 26 and OTP 27).
		// When the CI matrix declares an explicit OTP version we use an
		// OTP-specific Docker image tag (e.g. "1.18.4-otp-26") so that container
		// behaviour matches the tested environment.
		if major, minor, otp := detectElixirVersionFromCI(dir); major > 0 {
			if otp > 0 {
				return fetchElixirOTPImageVersion(ctx, client, image, major, minor, otp)
			}
			return fetchLatestImageVersionWithMajorMinorConstraint(ctx, client, image, major, minor)
		}

	case stackPython:
		// uvloop is incompatible with Python 3.14+; cap at 3.13 when it's present.
		if strings.Contains(install, "uvloop") {
			return fetchLatestImageVersionWithMajorMinorConstraint(ctx, client, image, 3, 13)
		}

	case stackDotNet:
		// .NET SDK images live on MCR, not Docker Hub, so we resolve the version
		// locally by inspecting global.json (SDK pin) or TargetFramework in
		// .csproj files rather than hitting the Docker Hub API.
		return detectDotNetVersion(dir), nil

	case stackCPP:
		// cimg/base uses date-based tags (e.g. "2024.01", "2024.01.1") rather
		// than semver, so the standard versionTagRe filter finds nothing.  Use
		// the dedicated date-tag fetcher instead.
		return fetchLatestCimgBaseDateVersion(ctx, client, image)

	case stackHaskell:
		// GHC releases frequently bump the bundled `base` library major version,
		// which breaks dependencies that have not yet published upper-bound
		// relaxations.  For example GHC 9.14 ships base-4.22 but many packages
		// (e.g. indexed-traversable-instances) still declare `base < 4.22`.
		// Cap to the highest explicit GHC version found in CI workflow files so
		// we don't inadvertently pick up a newly-released GHC that the project
		// has not yet been tested against.  Fall back to GHC 9.10 (a stable,
		// widely-supported LTS-style release) when no CI hint is available.
		const ghcFallbackMajor = 9
		const ghcFallbackMinor = 10
		maj, minor := detectHaskellGHCVersionFromCI(dir)
		if maj == 0 {
			maj = ghcFallbackMajor
			minor = ghcFallbackMinor
		}
		return fetchLatestImageVersionWithMajorMinorConstraint(ctx, client, image, maj, minor)
	}

	return fetchLatestImageVersion(ctx, client, image)
}

// findDotNetProjectSubdir returns a relative subdirectory path (e.g. "Src")
// if the .sln/.slnx/.csproj files are not at the repo root but found one level
// down.  Returns "" when solution/project files exist directly under dir.
func findDotNetProjectSubdir(dir string) string {
	// If root already has a solution or project file, no subdirectory needed.
	for _, pattern := range []string{"*.sln", "*.slnx", "*.csproj"} {
		if matches, _ := filepath.Glob(filepath.Join(dir, pattern)); len(matches) > 0 {
			return ""
		}
	}

	// Search one level of subdirectories for a solution or project file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() || skipDirs[entry.Name()] {
			continue
		}
		subdir := entry.Name()
		for _, pattern := range []string{"*.sln", "*.slnx", "*.csproj"} {
			if matches, _ := filepath.Glob(filepath.Join(dir, subdir, pattern)); len(matches) > 0 {
				return subdir
			}
		}
	}
	return ""
}

// detectDotNetVersion determines the appropriate .NET SDK major.minor version
// for the repository at dir.  It checks, in order:
//  1. global.json sdk.version field (explicit SDK pin), capped at the highest
//     TFM used by test projects so that the chosen image ships the runtime
//     that the compiled test binaries actually require.
//  2. The highest net<N>.0 TargetFramework found across all .csproj files
//  3. A default of "8.0" (current LTS)
func detectDotNetVersion(dir string) string {
	// 1. global.json – check both the repo root and the dotnet project subdir
	// (e.g. repos where solution/project files live under a "Src/" subdirectory
	// often place global.json there rather than at the root).
	globalJSONDirs := []string{dir}
	if subdir := findDotNetProjectSubdir(dir); subdir != "" {
		globalJSONDirs = append(globalJSONDirs, filepath.Join(dir, subdir))
	}
	for _, searchDir := range globalJSONDirs {
		if data, err := os.ReadFile(filepath.Join(searchDir, "global.json")); err == nil {
			var g struct {
				SDK struct {
					Version string `json:"version"`
				} `json:"sdk"`
			}
			if json.Unmarshal(data, &g) == nil && g.SDK.Version != "" {
				parts := strings.SplitN(g.SDK.Version, ".", 3)
				if len(parts) >= 2 {
					// global.json pins a required SDK version — this is a hard
					// minimum. The dotnet/sdk:N image ships previous runtimes
					// too, so --framework netM.0 (M < N) still works. Never
					// cap below the pinned SDK version or the build will fail
					// with "SDK not found".
					return parts[0] + "." + parts[1]
				}
			}
		}
	}

	// 2. Scan .csproj files for TargetFramework / TargetFrameworks.
	//    Match patterns like "net8.0", "net6.0" but not "netstandard2.0" which
	//    is not a runnable SDK version.
	netRe := regexp.MustCompile(`\bnet(\d+)\.0\b`)
	bestMajor := 0
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		if strings.HasSuffix(d.Name(), ".csproj") {
			data, readErr := os.ReadFile(path) //nolint:gosec // symlink traversal not a real risk: reading from a repo we cloned ourselves
			if readErr != nil {
				return nil
			}
			for _, m := range netRe.FindAllStringSubmatch(string(data), -1) {
				if major, convErr := strconv.Atoi(m[1]); convErr == nil && major >= 6 && major > bestMajor {
					bestMajor = major
				}
			}
		}
		return nil
	})
	if bestMajor >= 6 {
		return fmt.Sprintf("%d.0", bestMajor)
	}

	// 3. Default to .NET 8 LTS.
	return "8.0"
}

// detectDotNetTestFramework scans test .csproj files (identified by a
// reference to "Microsoft.NET.Test.Sdk") and returns the highest modern
// TargetFramework of the form "netN.0" (N >= 6) found among them.
// This TFM is passed to "dotnet test --framework" so that only the framework
// version that is present in the SDK image is exercised, avoiding failures
// from older target monikers such as netcoreapp2.1 or netcoreapp3.1 that are
// not bundled with modern dotnet/sdk images.
// Returns "" when no suitable TFM is found (caller omits --framework flag).
func detectDotNetTestFramework(dir string) string {
	netRe := regexp.MustCompile(`\bnet(\d+)\.0\b`)
	bestMajor := 0
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(d.Name(), ".csproj") {
			return nil
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // symlink traversal not a real risk: reading from a repo we cloned ourselves
		if readErr != nil {
			return nil
		}
		content := string(data)
		// Only consider projects that reference the .NET test SDK — these are
		// the test projects whose TFMs determine which runtimes must be present.
		if !strings.Contains(content, "Microsoft.NET.Test.Sdk") {
			return nil
		}
		for _, m := range netRe.FindAllStringSubmatch(content, -1) {
			if major, convErr := strconv.Atoi(m[1]); convErr == nil && major >= 6 && major > bestMajor {
				bestMajor = major
			}
		}
		return nil
	})
	if bestMajor >= 6 {
		return fmt.Sprintf("net%d.0", bestMajor)
	}
	return ""
}
