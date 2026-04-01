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

	"gotest.tools/v3/assert"

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
		case tc.want > 0:
			assert.Assert(t, got > 0, "compareVersions(%q, %q) = %d, want > 0", tc.a, tc.b, got)
		case tc.want < 0:
			assert.Assert(t, got < 0, "compareVersions(%q, %q) = %d, want < 0", tc.a, tc.b, got)
		default:
			assert.Equal(t, got, 0, "compareVersions(%q, %q) = %d, want 0", tc.a, tc.b, got)
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
		assert.Equal(t, got, tc.want)
	}
}

func TestIsTestRelatedExtra(t *testing.T) {
	t.Parallel()
	yes := []string{"test", "tests", "testing", "dev", "extras", "benchmark"}
	no := []string{"docs", "documentation", "lint", "linting", "format", "formatting",
		"style", "release", "publish", "build", "mypy", "typing", "typecheck", "all"}

	for _, name := range yes {
		assert.Assert(t, isTestRelatedExtra(name), "isTestRelatedExtra(%q) should be true", name)
	}
	for _, name := range no {
		assert.Assert(t, !isTestRelatedExtra(name), "isTestRelatedExtra(%q) should be false", name)
	}
}

func TestIsStrictlyTestGroup(t *testing.T) {
	t.Parallel()
	yes := []string{"test", "tests", "testing", "pytest", "coverage", "cov",
		"test-extras", "unit-test", "check", "pytest-plugins"}
	no := []string{"dev", "lint", "docs", "extras", "all", "ci"}

	for _, name := range yes {
		assert.Assert(t, isStrictlyTestGroup(name), "isStrictlyTestGroup(%q) should be true", name)
	}
	for _, name := range no {
		assert.Assert(t, !isStrictlyTestGroup(name), "isStrictlyTestGroup(%q) should be false", name)
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
		assert.Equal(t, got, tc.want, "parseJavaVersionConstraint(%q)", tc.input)
	}
}

// --- file-based tests ---

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0755))
	assert.NilError(t, os.WriteFile(path, []byte(content), 0600))
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
			assert.NilError(t, err)
			assert.Equal(t, got, tc.want)
		})
	}
}

func TestParsePyproject(t *testing.T) {
	t.Parallel()

	t.Run("missing file returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Assert(t, parsePyproject(t.TempDir()) == nil)
	})

	t.Run("parses hatch deps", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[tool.hatch.envs.default]
dependencies = ["pytest", "pytest-cov"]
`)
		deps := detectHatchTestDependencies(dir)
		assert.Equal(t, len(deps), 2, "unexpected hatch deps: %v", deps)
		assert.Equal(t, deps[0], "pytest")
		assert.Equal(t, deps[1], "pytest-cov")
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
		assert.Equal(t, len(extras), 1, "expected [test], got %v", extras)
		assert.Equal(t, extras[0], "test")
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
		assert.Equal(t, len(deps), 2, "expected 2 deps, got %v", deps)
		seen := map[string]bool{}
		for _, d := range deps {
			seen[d] = true
		}
		assert.Assert(t, seen["pytest>=8"] && seen["pytest-cov"], "missing expected deps, got %v", deps)
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
		assert.Equal(t, len(groups), 1, "expected [test], got %v", groups)
		assert.Equal(t, groups[0], "test")
	})

	t.Run("parses uv workspace members", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[tool.uv.workspace]
members = ["packages/foo", "packages/bar"]
`)
		members := detectUVWorkspaceMembers(dir)
		assert.Equal(t, len(members), 2, "expected 2 members, got %v", members)
	})

	t.Run("dependency-groups inline tables are skipped", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[dependency-groups]
test = ["pytest", {include-group = "dev"}]
`)
		deps := extractDepsFromDependencyGroups(dir)
		assert.Equal(t, len(deps), 1, "expected [pytest] (inline table skipped), got %v", deps)
		assert.Equal(t, deps[0], "pytest")
	})
}

func TestBuildUVSyncCommand(t *testing.T) {
	t.Parallel()

	t.Run("no extras or groups", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `[project]\nname = "foo"\n`)
		assert.Equal(t, buildUVSyncCommand(dir), "uv sync")
	})

	t.Run("with test group", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[dependency-groups]
test = ["pytest"]
`)
		assert.Equal(t, buildUVSyncCommand(dir), "uv sync --no-default-groups --group test")
	})

	t.Run("with optional dependency extra", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[project.optional-dependencies]
test = ["pytest"]
`)
		assert.Equal(t, buildUVSyncCommand(dir), "uv sync --extra test")
	})
}

func TestHasRustWorkspaceMember(t *testing.T) {
	t.Parallel()

	t.Run("no workspace", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `[project]\nname = "foo"\n`)
		assert.Assert(t, !hasRustWorkspaceMember(dir))
	})

	t.Run("workspace with rust member", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[tool.uv.workspace]
members = ["crates/foo"]
`)
		writeFile(t, dir, "crates/foo/Cargo.toml", `[package]\nname="foo"\n`)
		assert.Assert(t, hasRustWorkspaceMember(dir))
	})

	t.Run("workspace without rust member", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pyproject.toml", `
[tool.uv.workspace]
members = ["packages/bar"]
`)
		assert.NilError(t, os.MkdirAll(filepath.Join(dir, "packages/bar"), 0755))
		assert.Assert(t, !hasRustWorkspaceMember(dir))
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
			assert.Equal(t, detectGoModuleName(dir), tc.want)
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
			assert.Equal(t, maj, tc.wantMaj)
			assert.Equal(t, minor, tc.wantMin)
		})
	}
}

func TestDetectGoDartSassDep(t *testing.T) {
	t.Parallel()

	t.Run("no dep", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "go.mod", "module foo\n\nrequire (\n\tgithub.com/something/else v1.0.0\n)\n")
		assert.Assert(t, !detectGoDartSassDep(dir))
	})

	t.Run("godartsass dep", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "go.mod", "module foo\n\nrequire (\n\tgithub.com/bep/godartsass v1.0.0\n)\n")
		assert.Assert(t, detectGoDartSassDep(dir))
	})
}

func TestDetectNodeTestCommand(t *testing.T) {
	t.Parallel()

	t.Run("package has test script", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "package.json", `{"scripts":{"test":"jest"}}`)
		assert.Equal(t, detectNodeTestCommand(dir, "npm"), "npm test")
	})

	t.Run("nx project", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "package.json", `{"scripts":{}}`)
		writeFile(t, dir, "nx.json", `{}`)
		assert.Equal(t, detectNodeTestCommand(dir, "npm"), "npm nx run-many --target=test")
	})

	t.Run("no package.json falls back to pkgMgr test", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		assert.Equal(t, detectNodeTestCommand(dir, "yarn"), "yarn test")
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
			assert.Equal(t, detectNodeMaxVersion(dir), tc.want)
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
		assert.Equal(t, len(got), 0, "expected no skip modules, got %v", got)
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
		assert.Equal(t, len(got), 2, "expected 2 skip modules, got %v", got)
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
		assert.Equal(t, detectJavaMaxVersion(dir), 17)
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
		assert.Equal(t, detectJavaMaxVersion(dir), 21)
	})

	t.Run("no pom.xml returns -1", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, detectJavaMaxVersion(t.TempDir()), -1)
	})
}

func TestDetectCommands(t *testing.T) {
	t.Parallel()

	t.Run("go module", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "go.mod", "module github.com/foo/bar\n\ngo 1.22\n")
		install, test, deps := detectCommands(dir, stackGo)
		assert.Equal(t, install, "go mod download")
		assert.Equal(t, test, "go test -p 1 ./...")
		assert.Assert(t, len(deps) > 0, "expected system deps for go")
	})

	t.Run("python uv.lock", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "uv.lock", "# uv lockfile\n")
		install, testCmd, _ := detectCommands(dir, stackPython)
		assert.Equal(t, install, "uv sync")
		assert.Equal(t, testCmd, "uv run pytest")
	})

	t.Run("python requirements.txt", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "requirements.txt", "pytest\n")
		install, _, _ := detectCommands(dir, stackPython)
		assert.Equal(t, install, "pip install -r requirements.txt")
	})

	t.Run("javascript yarn", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "yarn.lock", "")
		writeFile(t, dir, "package.json", `{"scripts":{"test":"jest"}}`)
		install, testCmd, _ := detectCommands(dir, stackJavaScript)
		assert.Equal(t, install, "yarn install")
		assert.Equal(t, testCmd, "yarn test")
	})

	t.Run("unknown stack", func(t *testing.T) {
		t.Parallel()
		install, test, _ := detectCommands(t.TempDir(), stackUnknown)
		assert.Equal(t, install, stackUnknown)
		assert.Equal(t, test, stackUnknown)
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
		// Split-COPY pattern: go.mod/go.sum first so the download layer is
		// cached independently of source changes (e.g. Dockerfile.test, env.json).
		assertContains(t, content, "COPY --chown=circleci:circleci go.mod go.sum ./")
		assertContains(t, content, "RUN go mod download")
		assertContains(t, content, "COPY --chown=circleci:circleci . .")
		// Dep-ordered per-package loop to bound peak GOTMPDIR usage.
		assertContains(t, content, "go list -deps ./... | grep -Fxf")
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
		assert.Assert(t, !strings.Contains(content, "COPY --chown"), "non-cimg image should not have --chown")
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

// fakeTransport routes HTTP requests to an in-process handler without opening
// a real TCP connection, so tests don't need to intercept a real address.
type fakeTransport struct {
	handler http.Handler
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rr := httptest.NewRecorder()
	f.handler.ServeHTTP(rr, req)
	return rr.Result(), nil
}

// newFakeDockerHubClient returns a *httpcl.Client whose HTTP calls are handled
// by the provided handler instead of hitting the real Docker Hub.
func newFakeDockerHubClient(handler http.Handler) *httpcl.Client {
	return httpcl.New(httpcl.Config{Transport: &fakeTransport{handler: handler}})
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
		client := newFakeDockerHubClient(mux)
		tags, err := fetchAllImageVersions(context.Background(), client, "cimg/go")
		assert.NilError(t, err)
		assert.Equal(t, len(tags), 3, "expected 3 semver tags, got %v", tags)
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
		client := newFakeDockerHubClient(handler)
		tags, err := fetchAllImageVersions(context.Background(), client, "cimg/go")
		assert.NilError(t, err)
		assert.Equal(t, len(tags), 2, "expected 2 tags from 2 pages, got %v", tags)
		assert.Equal(t, callCount, 2, "expected 2 HTTP calls for pagination, got %d", callCount)
	})

	t.Run("invalid image name", func(t *testing.T) {
		client := newFakeDockerHubClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		_, err := fetchAllImageVersions(context.Background(), client, "noslash")
		assert.Assert(t, err != nil, "expected error for image without /")
	})

	t.Run("no version tags returns error", func(t *testing.T) {
		client := newFakeDockerHubClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(dockerHubTagsResponse{
				Results: []struct {
					Name string `json:"name"`
				}{{"latest"}, {"edge"}},
			})
		}))
		_, err := fetchAllImageVersions(context.Background(), client, "cimg/go")
		assert.Assert(t, err != nil, "expected error when no semver tags found")
	})
}

func TestFetchLatestImageVersionWithConstraint(t *testing.T) {
	client := newFakeDockerHubClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dockerHubTagsResponse{
			Results: []struct {
				Name string `json:"name"`
			}{
				{"22.0.0"}, {"20.18.0"}, {"20.17.0"}, {"18.20.0"},
			},
		})
	}))

	got, err := fetchLatestImageVersionWithConstraint(context.Background(), client, "cimg/node", 20)
	assert.NilError(t, err)
	assert.Equal(t, got, "20.18.0")
}

func TestFetchLatestImageVersionWithMajorMinorConstraint(t *testing.T) {
	client := newFakeDockerHubClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dockerHubTagsResponse{
			Results: []struct {
				Name string `json:"name"`
			}{
				{"1.24.0"}, {"1.23.5"}, {"1.23.4"}, {"1.22.9"},
			},
		})
	}))

	got, err := fetchLatestImageVersionWithMajorMinorConstraint(context.Background(), client, "cimg/go", 1, 23)
	assert.NilError(t, err)
	assert.Equal(t, got, "1.23.5")
}

// --- helpers ---

func assertContains(t *testing.T, content, substr string) {
	t.Helper()
	assert.Assert(t, strings.Contains(content, substr), "content does not contain %q\ncontent:\n%s", substr, content)
}
