package envbuilder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

// --- pure function tests ---

func TestCompareVersions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"2.0.0", "1.9.9", 1},
		{"1.9.9", "2.0.0", -1},
		{"1.2.4", "1.2.3", 1},
		{"1.2.3", "1.2.4", -1},
		{"10.0.0", "9.99.99", 1},
		{"bad", "1.0.0", 0}, // malformed — treated as equal
		{"1.0.0", "bad", 0},
		{"", "1.0.0", 0},
	}
	for _, tc := range cases {
		got := compareVersions(tc.a, tc.b)
		switch {
		case tc.want > 0 && got <= 0:
			t.Errorf("compareVersions(%q, %q) = %d, want > 0", tc.a, tc.b, got)
		case tc.want < 0 && got >= 0:
			t.Errorf("compareVersions(%q, %q) = %d, want < 0", tc.a, tc.b, got)
		case tc.want == 0 && got != 0:
			t.Errorf("compareVersions(%q, %q) = %d, want 0", tc.a, tc.b, got)
		}
	}
}

func TestHighestVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tags []string
		want string
	}{
		{[]string{"1.0.0"}, "1.0.0"},
		{[]string{"1.0.0", "2.0.0", "1.9.9"}, "2.0.0"},
		{[]string{"3.1.2", "3.1.10", "3.1.9"}, "3.1.10"},
		{[]string{"1.21.0", "1.22.1", "1.22.0"}, "1.22.1"},
	}
	for _, tc := range cases {
		got := highestVersion(tc.tags)
		if got != tc.want {
			t.Errorf("highestVersion(%v) = %q, want %q", tc.tags, got, tc.want)
		}
	}
}

func TestIsTestRelatedExtra(t *testing.T) {
	t.Parallel()
	yes := []string{"test", "tests", "testing", "dev", "extras", "benchmark"}
	no := []string{"docs", "documentation", "lint", "linting", "format", "formatting",
		"style", "release", "publish", "build", "mypy", "typing", "typecheck", "all"}

	for _, name := range yes {
		if !isTestRelatedExtra(name) {
			t.Errorf("isTestRelatedExtra(%q) = false, want true", name)
		}
	}
	for _, name := range no {
		if isTestRelatedExtra(name) {
			t.Errorf("isTestRelatedExtra(%q) = true, want false", name)
		}
	}
}

func TestIsStrictlyTestGroup(t *testing.T) {
	t.Parallel()
	yes := []string{"test", "tests", "testing", "pytest", "coverage", "cov",
		"test-extras", "unit-test", "check", "pytest-plugins"}
	no := []string{"dev", "lint", "docs", "extras", "all", "ci"}

	for _, name := range yes {
		if !isStrictlyTestGroup(name) {
			t.Errorf("isStrictlyTestGroup(%q) = false, want true", name)
		}
	}
	for _, name := range no {
		if isStrictlyTestGroup(name) {
			t.Errorf("isStrictlyTestGroup(%q) = true, want false", name)
		}
	}
}

func TestParseJavaVersionConstraint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  int
	}{
		{"[17,22)", 21}, // exclusive upper bound
		{"[17,22]", 22}, // inclusive upper bound
		{"[11,18)", 17},
		{"17", 17},    // single version — singleRe matches
		{"[17,)", 17}, // no numeric upper — falls through to singleRe which captures 17
		{"", -1},
		{"  ", -1},
		{"[8,21)", 20},
	}
	for _, tc := range cases {
		got := parseJavaVersionConstraint(tc.input)
		if got != tc.want {
			t.Errorf("parseJavaVersionConstraint(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// --- file-based tests ---

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDetectStack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		files map[string]string // relative path → content
		want  string
	}{
		{
			name:  "go indicator file",
			files: map[string]string{"go.mod": "module example.com/foo\n\ngo 1.21\n"},
			want:  stackGo,
		},
		{
			name:  "python indicator file",
			files: map[string]string{"requirements.txt": "pytest\n"},
			want:  stackPython,
		},
		{
			// tsconfig.json scores 10 for typescript; .ts source files add 1 each
			// package.json scores 10 for javascript — adding a .ts file breaks the tie
			name:  "typescript beats javascript with source file tiebreak",
			files: map[string]string{"tsconfig.json": "{}", "package.json": "{}", "index.ts": ""},
			want:  stackTypeScript,
		},
		{
			name: "source extensions tiebreak",
			files: map[string]string{
				"a.py": "", "b.py": "", "c.py": "",
				"d.go": "",
			},
			want: stackPython, // 3 .py > 1 .go
		},
		{
			name:  "empty dir",
			files: map[string]string{},
			want:  stackUnknown,
		},
		{
			name:  "rust indicator",
			files: map[string]string{"Cargo.toml": "[package]\nname=\"foo\"\n"},
			want:  stackRust,
		},
		{
			name:  "java pom",
			files: map[string]string{"pom.xml": "<project/>"},
			want:  stackJava,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range tc.files {
				writeFile(t, dir, name, content)
			}
			got, err := detectStack(dir)
			if err != nil {
				t.Fatalf("detectStack: %v", err)
			}
			if got != tc.want {
				t.Errorf("detectStack = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParsePyproject(t *testing.T) {
	t.Parallel()

	t.Run("missing file returns nil", func(t *testing.T) {
		t.Parallel()
		if parsePyproject(t.TempDir()) != nil {
			t.Error("expected nil for missing file")
		}
	})

	t.Run("parses hatch deps", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[tool.hatch.envs.default]
dependencies = ["pytest", "pytest-cov"]
`)
		deps := detectHatchTestDependencies(dir)
		if len(deps) != 2 || deps[0] != "pytest" || deps[1] != "pytest-cov" {
			t.Errorf("unexpected hatch deps: %v", deps)
		}
	})

	t.Run("parses optional-dependencies extras", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[project.optional-dependencies]
test = ["pytest"]
docs = ["sphinx"]
lint = ["ruff"]
`)
		extras := detectUVTestExtras(dir)
		if len(extras) != 1 || extras[0] != "test" {
			t.Errorf("expected [test], got %v", extras)
		}
	})

	t.Run("parses dependency-groups", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[dependency-groups]
test = ["pytest>=8", "pytest-cov"]
dev = ["black"]
`)
		deps := extractDepsFromDependencyGroups(dir)
		if len(deps) != 2 {
			t.Errorf("expected 2 deps, got %v", deps)
		}
		seen := map[string]bool{}
		for _, d := range deps {
			seen[d] = true
		}
		if !seen["pytest>=8"] || !seen["pytest-cov"] {
			t.Errorf("missing expected deps, got %v", deps)
		}
	})

	t.Run("detects test dependency groups", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[dependency-groups]
test = ["pytest"]
docs = ["sphinx"]
`)
		groups := detectTestDependencyGroups(dir)
		if len(groups) != 1 || groups[0] != "test" {
			t.Errorf("expected [test], got %v", groups)
		}
	})

	t.Run("parses uv workspace members", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[tool.uv.workspace]
members = ["packages/foo", "packages/bar"]
`)
		members := detectUVWorkspaceMembers(dir)
		if len(members) != 2 {
			t.Errorf("expected 2 members, got %v", members)
		}
	})

	t.Run("dependency-groups inline tables are skipped", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[dependency-groups]
test = ["pytest", {include-group = "dev"}]
`)
		deps := extractDepsFromDependencyGroups(dir)
		if len(deps) != 1 || deps[0] != "pytest" {
			t.Errorf("expected [pytest] (inline table skipped), got %v", deps)
		}
	})
}

func TestBuildUVSyncCommand(t *testing.T) {
	t.Parallel()

	t.Run("no extras or groups", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `[project]\nname = "foo"\n`)
		got := buildUVSyncCommand(dir)
		if got != "uv sync" {
			t.Errorf("got %q, want %q", got, "uv sync")
		}
	})

	t.Run("with test group", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[dependency-groups]
test = ["pytest"]
`)
		got := buildUVSyncCommand(dir)
		want := "uv sync --no-default-groups --group test"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("with optional dependency extra", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[project.optional-dependencies]
test = ["pytest"]
`)
		got := buildUVSyncCommand(dir)
		want := "uv sync --extra test"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestHasRustWorkspaceMember(t *testing.T) {
	t.Parallel()

	t.Run("no workspace", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `[project]\nname = "foo"\n`)
		if hasRustWorkspaceMember(dir) {
			t.Error("expected false for project with no workspace")
		}
	})

	t.Run("workspace with rust member", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[tool.uv.workspace]
members = ["crates/foo"]
`)
		writeFile(t, dir, "crates/foo/Cargo.toml", `[package]\nname="foo"\n`)
		if !hasRustWorkspaceMember(dir) {
			t.Error("expected true for workspace with Cargo.toml member")
		}
	})

	t.Run("workspace without rust member", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[tool.uv.workspace]
members = ["packages/bar"]
`)
		if err := os.MkdirAll(filepath.Join(dir, "packages/bar"), 0755); err != nil {
			t.Fatal(err)
		}
		if hasRustWorkspaceMember(dir) {
			t.Error("expected false for workspace member without Cargo.toml")
		}
	})
}

func TestDetectGoModuleName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		gomod string
		want  string
	}{
		{"module github.com/foo/bar\n\ngo 1.21\n", "bar"},
		{"module example.com/my-app\n", "my-app"},
		{"module simple\n", "simple"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if tc.gomod != "" {
				writeFile(t, dir, "go.mod", tc.gomod)
			}
			got := detectGoModuleName(dir)
			if got != tc.want {
				t.Errorf("detectGoModuleName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDetectGoVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		gomod            string
		wantMaj, wantMin int
	}{
		{"module foo\n\ngo 1.21\n", 1, 21},
		{"module foo\n\ngo 1.22.1\n", 1, 22},
		{"module foo\n", 0, 0},
		{"", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.gomod, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if tc.gomod != "" {
				writeFile(t, dir, "go.mod", tc.gomod)
			}
			maj, minor := detectGoVersion(dir)
			if maj != tc.wantMaj || minor != tc.wantMin {
				t.Errorf("detectGoVersion = (%d, %d), want (%d, %d)", maj, minor, tc.wantMaj, tc.wantMin)
			}
		})
	}
}

func TestDetectGoDartSassDep(t *testing.T) {
	t.Parallel()

	t.Run("no dep", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "go.mod", "module foo\n\nrequire (\n\tgithub.com/something/else v1.0.0\n)\n")
		if detectGoDartSassDep(dir) {
			t.Error("expected false")
		}
	})

	t.Run("godartsass dep", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "go.mod", "module foo\n\nrequire (\n\tgithub.com/bep/godartsass v1.0.0\n)\n")
		if !detectGoDartSassDep(dir) {
			t.Error("expected true for godartsass")
		}
	})
}

func TestDetectNodeTestCommand(t *testing.T) {
	t.Parallel()

	t.Run("package has test script", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "package.json", `{"scripts":{"test":"jest"}}`)
		got := detectNodeTestCommand(dir, "npm")
		if got != "npm test" {
			t.Errorf("got %q, want %q", got, "npm test")
		}
	})

	t.Run("nx project", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "package.json", `{"scripts":{}}`)
		writeFile(t, dir, "nx.json", `{}`)
		got := detectNodeTestCommand(dir, "npm")
		if got != "npm nx run-many --target=test" {
			t.Errorf("got %q, want %q", got, "npm nx run-many --target=test")
		}
	})

	t.Run("no package.json falls back to pkgMgr test", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		got := detectNodeTestCommand(dir, "yarn")
		if got != "yarn test" {
			t.Errorf("got %q, want %q", got, "yarn test")
		}
	})
}

func TestDetectNodeMaxVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pkg  string
		want int
	}{
		// The regex \b(\d+)\.\d only matches majors from N.N patterns,
		// so ">=18.0.0 <21" yields 18 (only 18.0 matches the pattern).
		{`{"engines":{"node":">=18.0.0 <21"}}`, 18},
		{`{"engines":{"node":">=20.0.0"}}`, 20},
		{`{"engines":{}}`, -1},
		{`{}`, -1},
	}
	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeFile(t, dir, "package.json", tc.pkg)
			got := detectNodeMaxVersion(dir)
			if got != tc.want {
				t.Errorf("detectNodeMaxVersion = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDetectMavenSkipModules(t *testing.T) {
	t.Parallel()

	t.Run("no skip modules", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pom.xml", `<project><modules><module>core</module><module>web</module></modules></project>`)
		got := detectMavenSkipModules(dir)
		if len(got) != 0 {
			t.Errorf("expected no skip modules, got %v", got)
		}
	})

	t.Run("skips graal and android modules", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pom.xml", `
<project>
  <modules>
    <module>core</module>
    <module>graal-native</module>
    <module>android-support</module>
    <module>web</module>
  </modules>
</project>`)
		got := detectMavenSkipModules(dir)
		if len(got) != 2 {
			t.Errorf("expected 2 skip modules, got %v", got)
		}
	})
}

func TestDetectJavaMaxVersion(t *testing.T) {
	t.Parallel()

	t.Run("maven compiler source property", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pom.xml", `
<project>
  <properties>
    <maven.compiler.source>17</maven.compiler.source>
  </properties>
</project>`)
		got := detectJavaMaxVersion(dir)
		if got != 17 {
			t.Errorf("got %d, want 17", got)
		}
	})

	t.Run("enforcer plugin range", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pom.xml", `
<project>
  <build>
    <plugins>
      <plugin>
        <artifactId>maven-enforcer-plugin</artifactId>
        <configuration>
          <rules>
            <requireJavaVersion>
              <version>[17,22)</version>
            </requireJavaVersion>
          </rules>
        </configuration>
      </plugin>
    </plugins>
  </build>
</project>`)
		got := detectJavaMaxVersion(dir)
		if got != 21 {
			t.Errorf("got %d, want 21", got)
		}
	})

	t.Run("no pom.xml returns -1", func(t *testing.T) {
		t.Parallel()
		got := detectJavaMaxVersion(t.TempDir())
		if got != -1 {
			t.Errorf("got %d, want -1", got)
		}
	})
}

func TestDetectCommands(t *testing.T) {
	t.Parallel()

	t.Run("go module", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "go.mod", "module github.com/foo/bar\n\ngo 1.22\n")
		install, test, deps := detectCommands(dir, stackGo)
		if install != "go mod download" {
			t.Errorf("install = %q, want %q", install, "go mod download")
		}
		if test != "go test -p 1 ./..." {
			t.Errorf("test = %q", test)
		}
		if len(deps) == 0 {
			t.Error("expected system deps for go")
		}
	})

	t.Run("python uv.lock", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "uv.lock", "# uv lockfile\n")
		install, testCmd, deps := detectCommands(dir, stackPython)
		if install != "uv sync" {
			t.Errorf("install = %q, want %q", install, "uv sync")
		}
		if testCmd != "uv run pytest" {
			t.Errorf("test = %q", testCmd)
		}
		_ = deps
	})

	t.Run("python requirements.txt", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "requirements.txt", "pytest\n")
		install, _, _ := detectCommands(dir, stackPython)
		if install != "pip install -r requirements.txt" {
			t.Errorf("install = %q", install)
		}
	})

	t.Run("javascript yarn", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "yarn.lock", "")
		writeFile(t, dir, "package.json", `{"scripts":{"test":"jest"}}`)
		install, testCmd, _ := detectCommands(dir, stackJavaScript)
		if install != "yarn install" {
			t.Errorf("install = %q", install)
		}
		if testCmd != "yarn test" {
			t.Errorf("test = %q", testCmd)
		}
	})

	t.Run("unknown stack", func(t *testing.T) {
		t.Parallel()
		install, test, _ := detectCommands(t.TempDir(), stackUnknown)
		if install != stackUnknown || test != stackUnknown {
			t.Errorf("expected unknown commands for unknown stack, got install=%q test=%q", install, test)
		}
	})
}

func TestDockerfileContent(t *testing.T) {
	t.Parallel()

	t.Run("basic go dockerfile", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "go.mod", "module github.com/foo/bar\n\ngo 1.22\n")
		env := &Environment{
			Stack:        stackGo,
			Install:      "go mod download",
			Test:         "go test ./...",
			Image:        "cimg/go",
			ImageVersion: "1.22.3",
		}
		content := dockerfileContent(dir, env)
		assertContains(t, content, "FROM cimg/go:1.22.3")
		assertContains(t, content, "WORKDIR /app/bar") // last segment of module
		assertContains(t, content, "COPY --chown=circleci:circleci . .")
		assertContains(t, content, "RUN go mod download")
		assertContains(t, content, "go list ./... | while IFS= read -r pkg")
	})

	t.Run("non-cimg image uses plain COPY", func(t *testing.T) {
		t.Parallel()
		env := &Environment{
			Stack:        stackPython,
			Install:      "pip install -r requirements.txt",
			Test:         "pytest",
			Image:        "python",
			ImageVersion: "3.12.0",
		}
		content := dockerfileContent(t.TempDir(), env)
		assertContains(t, content, "FROM python:3.12.0")
		assertContains(t, content, "COPY . .")
		if strings.Contains(content, "COPY --chown") {
			t.Error("non-cimg image should not have --chown")
		}
	})

	t.Run("system dep injects RUN", func(t *testing.T) {
		t.Parallel()
		env := &Environment{
			Stack:        stackPython,
			Install:      "pip install uv",
			Test:         "uv run pytest",
			SystemDeps:   []string{"uv"},
			Image:        "cimg/python",
			ImageVersion: "3.12.0",
		}
		content := dockerfileContent(t.TempDir(), env)
		assertContains(t, content, "RUN pip install uv")
	})

	t.Run("rust workspace member sets cargo env", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[tool.uv.workspace]
members = ["crates/mypkg"]
`)
		writeFile(t, dir, "crates/mypkg/Cargo.toml", `[package]\nname="mypkg"\n`)
		env := &Environment{
			Stack:        stackPython,
			Install:      "uv sync",
			Test:         "uv run pytest",
			Image:        "cimg/python",
			ImageVersion: "3.12.0",
		}
		content := dockerfileContent(dir, env)
		assertContains(t, content, "ENV CARGO_TARGET_DIR=/tmp/cargo-target")
		assertContains(t, content, "ENV UV_CACHE_DIR=/tmp/uv-cache")
	})

	t.Run("sudo apt-get for cimg system deps", func(t *testing.T) {
		t.Parallel()
		env := &Environment{
			Stack:        stackGo,
			Install:      "go mod download",
			Test:         "go test ./...",
			SystemDeps:   []string{"git"},
			Image:        "cimg/go",
			ImageVersion: "1.22.0",
		}
		content := dockerfileContent(t.TempDir(), env)
		assertContains(t, content, "sudo apt-get")
	})
}

// --- network tests (fake Docker Hub) ---

// mockTransport routes HTTP requests to an in-process handler.
type mockTransport struct {
	handler http.Handler
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rr := httptest.NewRecorder()
	m.handler.ServeHTTP(rr, req)
	return rr.Result(), nil
}

// withMockDockerHub replaces dockerHubClient for the duration of the test.
func withMockDockerHub(t *testing.T, handler http.Handler) {
	t.Helper()
	original := dockerHubClient
	dockerHubClient = httpcl.New(httpcl.Config{
		Transport: &mockTransport{handler: handler},
	})
	t.Cleanup(func() { dockerHubClient = original })
}

func TestFetchAllImageVersions(t *testing.T) {
	t.Run("single page", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/v2/repositories/cimg/go/tags", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(dockerHubTagsResponse{
				Results: []struct {
					Name string `json:"name"`
				}{
					{"1.22.3"},
					{"1.22.2"},
					{"latest"}, // should be filtered out (not semver)
					{"1.21.0"},
				},
			})
		})
		withMockDockerHub(t, mux)

		tags, err := fetchAllImageVersions(context.Background(), "cimg/go")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(tags) != 3 {
			t.Errorf("expected 3 semver tags, got %v", tags)
		}
	})

	t.Run("pagination", func(t *testing.T) {
		callCount := 0
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			w.Header().Set("Content-Type", "application/json")
			resp := dockerHubTagsResponse{}
			if r.URL.Query().Get("page") == "2" {
				resp.Results = []struct {
					Name string `json:"name"`
				}{{"1.21.9"}}
				// no Next — stop pagination
			} else {
				resp.Results = []struct {
					Name string `json:"name"`
				}{{"1.22.0"}}
				resp.Next = "https://hub.docker.com/v2/repositories/cimg/go/tags?page=2"
			}
			_ = json.NewEncoder(w).Encode(resp)
		})
		withMockDockerHub(t, handler)

		tags, err := fetchAllImageVersions(context.Background(), "cimg/go")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(tags) != 2 {
			t.Errorf("expected 2 tags from 2 pages, got %v", tags)
		}
		if callCount != 2 {
			t.Errorf("expected 2 HTTP calls for pagination, got %d", callCount)
		}
	})

	t.Run("invalid image name", func(t *testing.T) {
		_, err := fetchAllImageVersions(context.Background(), "noslash")
		if err == nil {
			t.Error("expected error for image without /")
		}
	})

	t.Run("no version tags returns error", func(t *testing.T) {
		withMockDockerHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(dockerHubTagsResponse{
				Results: []struct {
					Name string `json:"name"`
				}{{"latest"}, {"edge"}},
			})
		}))
		_, err := fetchAllImageVersions(context.Background(), "cimg/go")
		if err == nil {
			t.Error("expected error when no semver tags found")
		}
	})
}

func TestFetchLatestImageVersionWithConstraint(t *testing.T) {
	withMockDockerHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dockerHubTagsResponse{
			Results: []struct {
				Name string `json:"name"`
			}{
				{"22.0.0"}, {"20.18.0"}, {"20.17.0"}, {"18.20.0"},
			},
		})
	}))

	got, err := fetchLatestImageVersionWithConstraint(context.Background(), "cimg/node", 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "20.18.0" {
		t.Errorf("got %q, want %q", got, "20.18.0")
	}
}

func TestFetchLatestImageVersionWithMajorMinorConstraint(t *testing.T) {
	withMockDockerHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dockerHubTagsResponse{
			Results: []struct {
				Name string `json:"name"`
			}{
				{"1.24.0"}, {"1.23.5"}, {"1.23.4"}, {"1.22.9"},
			},
		})
	}))

	got, err := fetchLatestImageVersionWithMajorMinorConstraint(context.Background(), "cimg/go", 1, 23)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "1.23.5" {
		t.Errorf("got %q, want %q", got, "1.23.5")
	}
}

// --- helpers ---

func assertContains(t *testing.T, content, substr string) {
	t.Helper()
	if !strings.Contains(content, substr) {
		t.Errorf("content does not contain %q\ncontent:\n%s", substr, content)
	}
}
