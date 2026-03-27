package envbuilder

import (
	"context"
	"encoding/json"
	"encoding/xml"
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
	stackUnknown    = "unknown"

	cimgPrefix = "cimg/"
)

// Environment describes the detected tech stack and build configuration for a repository.
type Environment struct {
	Stack          string   `json:"stack"`
	Install        string   `json:"install"`
	Test           string   `json:"test"`
	SystemDeps     []string `json:"system_deps"`
	Image          string   `json:"image"`
	ImageVersion   string   `json:"image_version"`
	DockerfilePath string   `json:"dockerfile_path"`
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
	"Gemfile":          stackRuby,
	"composer.json":    stackPHP,
}

var sourceExtensions = map[string]string{
	".py":   stackPython,
	".go":   stackGo,
	".js":   stackJavaScript,
	".ts":   stackTypeScript,
	".rs":   stackRust,
	".java": stackJava,
	".rb":   stackRuby,
	".php":  stackPHP,
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
	stackRuby:       cimgPrefix + "ruby",
	stackPHP:        cimgPrefix + "php",
}

var versionTagRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

var dockerHubClient = httpcl.New(httpcl.Config{})

type dockerHubTagsResponse struct {
	Results []struct {
		Name string `json:"name"`
	} `json:"results"`
	Next string `json:"next"`
}

func fetchAllImageVersions(ctx context.Context, image string) ([]string, error) {
	parts := strings.SplitN(image, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid image name: %s", image)
	}

	var allTags []string
	route := fmt.Sprintf(
		"https://hub.docker.com/v2/repositories/%s/%s/tags?page_size=100&ordering=last_updated",
		parts[0], parts[1],
	)

	for route != "" && len(allTags) < 300 {
		var page dockerHubTagsResponse
		if _, err := dockerHubClient.Call(ctx, httpcl.NewRequest("GET", route, httpcl.JSONDecoder(&page))); err != nil {
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

func fetchLatestImageVersion(ctx context.Context, image string) (string, error) {
	allTags, err := fetchAllImageVersions(ctx, image)
	if err != nil {
		return "", err
	}
	return highestVersion(allTags), nil
}

func fetchLatestImageVersionWithConstraint(ctx context.Context, image string, maxMajor int) (string, error) {
	allTags, err := fetchAllImageVersions(ctx, image)
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
func fetchLatestImageVersionWithMajorMinorConstraint(ctx context.Context, image string, maxMajor, maxMinor int) (string, error) {
	allTags, err := fetchAllImageVersions(ctx, image)
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
		fmt.Fprintf(os.Stderr, "chunk: compareVersions: malformed version %q or %q\n", a, b)
		return 0
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
	"composer":    "php -r \"copy('https://getcomposer.org/installer', 'composer-setup.php');\" && php composer-setup.php --install-dir=/usr/local/bin --filename=composer && php -r \"unlink('composer-setup.php');\"",
	"git":         "apt-get update && apt-get install -y git --no-install-recommends && rm -rf /var/lib/apt/lists/*",
	"dart-sass":   "apt-get update && apt-get install -y curl --no-install-recommends && rm -rf /var/lib/apt/lists/* && ARCH=$(uname -m) && case \"$ARCH\" in x86_64) SASS_ARCH=linux-x64 ;; aarch64) SASS_ARCH=linux-arm64 ;; *) echo \"Unsupported arch: $ARCH\" && exit 1 ;; esac && curl -fsSL \"https://github.com/sass/dart-sass/releases/download/1.80.3/dart-sass-1.80.3-${SASS_ARCH}.tar.gz\" | sudo tar -xz -C /usr/local && sudo chmod -R 755 /usr/local/dart-sass && printf '#!/bin/sh\\nexec /usr/local/dart-sass/sass \"$@\"\\n' | sudo tee /usr/local/bin/sass > /dev/null && sudo chmod +x /usr/local/bin/sass",
	"asciidoctor": "apt-get update && apt-get install -y asciidoctor --no-install-recommends && rm -rf /var/lib/apt/lists/*",
	"pandoc":      "apt-get update && apt-get install -y pandoc --no-install-recommends && rm -rf /var/lib/apt/lists/*",
	"rst2html":    "apt-get update && apt-get install -y python3-docutils --no-install-recommends && rm -rf /var/lib/apt/lists/*",
}

func generateDockerfile(dir string, env *Environment) (string, error) {
	var sb strings.Builder

	fromLine := "FROM " + env.Image + ":" + env.ImageVersion
	sb.WriteString(fromLine + "\n")

	for _, dep := range env.SystemDeps {
		if cmd, ok := extraDepInstalls[dep]; ok {
			// cimg/* images run as a non-root user (circleci), so apt-get requires sudo.
			if strings.HasPrefix(env.Image, cimgPrefix) {
				cmd = strings.ReplaceAll(cmd, "apt-get", "sudo apt-get")
				cmd = strings.ReplaceAll(cmd, "rm -rf /var/lib/apt/lists/*", "sudo rm -rf /var/lib/apt/lists/*")
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
	workdir := "/app"
	if env.Stack == stackGo {
		if modName := detectGoModuleName(dir); modName != "" {
			workdir = "/app/" + modName
		}
	}
	sb.WriteString("\nWORKDIR " + workdir + "\n")
	if strings.HasPrefix(env.Image, cimgPrefix) {
		sb.WriteString("COPY --chown=circleci:circleci . .\n")
	} else {
		sb.WriteString("COPY . .\n")
	}
	sb.WriteString("\nRUN " + env.Install + "\n")
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
	// NOTE: do NOT add a pre-build RUN step that populates GOCACHE here.
	// Although pre-building test-variant archives into GOCACHE and baking
	// them into the image avoids GOTMPDIR writes at runtime, the GOCACHE
	// for a large project (e.g. Hugo) grows to several GB.  Docker's layer
	// export then fails with "no space left on device" when the host's
	// containerd storage cannot accommodate the oversized layer.  The
	// per-package CMD loop below achieves the same GOTMPDIR bound without
	// inflating the image.
	if env.Stack == stackGo {
		sb.WriteString("\nCMD go list ./... | while IFS= read -r pkg; do go test \"$pkg\" || exit 1; done\n")
	} else {
		sb.WriteString("\nCMD " + env.Test + "\n")
	}

	dockerfilePath := filepath.Join(dir, "Dockerfile.test")
	if err := os.WriteFile(dockerfilePath, []byte(sb.String()), 0644); err != nil {
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
	"pytest",
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

func detectCommands(dir, stack string) (string, string, []string) {
	var install, test string
	var systemDeps []string

	switch stack {
	case stackPython:
		switch {
		case fileExists(dir, "uv.lock"):
			install = buildUVSyncCommand(dir)
			test = "uv run pytest"
			systemDeps = []string{"uv"}
		case fileExists(dir, "Pipfile"):
			install = "pipenv install --dev"
			test = "pipenv run pytest"
			systemDeps = []string{stackPython, "pipenv"}
		case fileExists(dir, "requirements.txt"):
			install = "pip install -r requirements.txt"
			test = "pytest"
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
			test = "pytest"
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
			systemDeps = []string{stackJava}
		}

	case stackRuby:
		install = "bundle install"
		test = "bundle exec rspec"
		systemDeps = []string{stackRuby, "bundle"}

	case stackPHP:
		install = "composer install"
		test = "vendor/bin/phpunit"
		systemDeps = []string{stackPHP, "composer"}

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
			data, err := os.ReadFile(path) //nolint:gosec
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
// It also writes a Dockerfile.test to dir.
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
		imageVersion, err = detectImageVersion(ctx, dir, stack, image, install)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch image version: %w", err)
		}
	}

	env := &Environment{
		Stack:        stack,
		Install:      install,
		Test:         test,
		SystemDeps:   systemDeps,
		Image:        image,
		ImageVersion: imageVersion,
	}

	dockerfilePath, err := generateDockerfile(dir, env)
	if err != nil {
		return nil, err
	}
	env.DockerfilePath = dockerfilePath

	return env, nil
}

// detectImageVersion fetches the appropriate CircleCI image version for the detected stack.
func detectImageVersion(ctx context.Context, dir, stack, image, install string) (string, error) {
	switch stack {
	case stackGo:
		// Cap to the major.minor declared in go.mod. Go 1.23 is used as a floor
		// for reliable timer precision in containers.
		const goMinorFloor = 23
		if major, minor := detectGoVersion(dir); major > 0 {
			if major == 1 && minor < goMinorFloor {
				minor = goMinorFloor
			}
			return fetchLatestImageVersionWithMajorMinorConstraint(ctx, image, major, minor)
		}

	case stackJavaScript, stackTypeScript:
		if fileExists(dir, "package.json") {
			if maxNode := detectNodeMaxVersion(dir); maxNode > 0 {
				return fetchLatestImageVersionWithConstraint(ctx, image, maxNode)
			}
		}

	case stackJava:
		if fileExists(dir, "pom.xml") {
			if maxJava := detectJavaMaxVersion(dir); maxJava > 0 {
				return fetchLatestImageVersionWithConstraint(ctx, image, maxJava)
			}
		}

	case stackPython:
		// uvloop is incompatible with Python 3.14+; cap at 3.13 when it's present.
		if strings.Contains(install, "uvloop") {
			return fetchLatestImageVersionWithMajorMinorConstraint(ctx, image, 3, 13)
		}
	}

	return fetchLatestImageVersion(ctx, image)
}
