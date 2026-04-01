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
		{
			name:  "elixir mix.exs",
			files: map[string]string{"mix.exs": "defmodule MyApp.MixProject do\nend\n"},
			want:  stackElixir,
		},
		{
			name:  "elixir source file tiebreak",
			files: map[string]string{"lib/foo.ex": "", "lib/bar.ex": "", "lib/baz.ex": ""},
			want:  stackElixir,
		},
		{
			name:  "dotnet csproj",
			files: map[string]string{"MyApp.csproj": "<Project Sdk=\"Microsoft.NET.Sdk\"/>"},
			want:  stackDotNet,
		},
		{
			name:  "dotnet sln",
			files: map[string]string{"MyApp.sln": ""},
			want:  stackDotNet,
		},
		{
			name:  "dart pubspec.yaml",
			files: map[string]string{"pubspec.yaml": "name: my_package\n"},
			want:  stackDart,
		},
		{
			name:  "scala build.sbt",
			files: map[string]string{"build.sbt": "name := \"myapp\"\n"},
			want:  stackScala,
		},
		{
			name:  "haskell stack.yaml",
			files: map[string]string{"stack.yaml": "resolver: lts-21.0\n"},
			want:  stackHaskell,
		},
		{
			name:  "haskell cabal.project",
			files: map[string]string{"cabal.project": "packages: .\n"},
			want:  stackHaskell,
		},
		{
			name:  "cpp CMakeLists.txt",
			files: map[string]string{"CMakeLists.txt": "cmake_minimum_required(VERSION 3.20)\n"},
			want:  stackCPP,
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

	t.Run("elixir", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "mix.exs", "defmodule MyApp.MixProject do\nend\n")
		install, test, _ := detectCommands(dir, stackElixir)
		assert.Assert(t, strings.Contains(install, "mix deps.get"), "expected mix deps.get in install, got: %s", install)
		assert.Equal(t, test, "mix test")
	})

	t.Run("dotnet no subdir", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		install, test, _ := detectCommands(dir, stackDotNet)
		assert.Equal(t, install, "dotnet restore")
		assert.Assert(t, strings.HasPrefix(test, "dotnet test"), "expected dotnet test prefix, got: %s", test)
	})

	t.Run("dotnet with framework flag", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "MyTests.csproj", `<Project>
  <ItemGroup>
    <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.0.0" />
  </ItemGroup>
  <PropertyGroup>
    <TargetFramework>net8.0</TargetFramework>
  </PropertyGroup>
</Project>`)
		_, test, _ := detectCommands(dir, stackDotNet)
		assert.Assert(t, strings.Contains(test, "--framework net8.0"), "expected --framework net8.0, got: %s", test)
	})

	t.Run("dart single package", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pubspec.yaml", "name: my_package\n")
		install, test, _ := detectCommands(dir, stackDart)
		assert.Equal(t, install, "dart pub get")
		assert.Equal(t, test, cmdDartTest)
	})

	t.Run("dart monorepo with test dirs", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pkgs/foo/pubspec.yaml", "name: foo\n")
		assert.NilError(t, os.MkdirAll(filepath.Join(dir, "pkgs", "foo", "test"), 0755))
		writeFile(t, dir, "pkgs/bar/pubspec.yaml", "name: bar\n")
		assert.NilError(t, os.MkdirAll(filepath.Join(dir, "pkgs", "bar", "test"), 0755))
		install, test, _ := detectCommands(dir, stackDart)
		assert.Assert(t, strings.Contains(install, "dart pub get"), "expected dart pub get in install, got: %s", install)
		assert.Assert(t, strings.Contains(test, "dart test"), "expected dart test in test, got: %s", test)
	})

	t.Run("dart monorepo flutter package excluded", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pkgs/plain/pubspec.yaml", "name: plain\n")
		assert.NilError(t, os.MkdirAll(filepath.Join(dir, "pkgs", "plain", "test"), 0755))
		writeFile(t, dir, "pkgs/flutter_pkg/pubspec.yaml", "name: flutter_pkg\ndependencies:\n  flutter:\n    sdk: flutter\n")
		install, _, _ := detectCommands(dir, stackDart)
		assert.Assert(t, !strings.Contains(install, "flutter_pkg"), "flutter package should be excluded, got: %s", install)
		assert.Assert(t, strings.Contains(install, "plain"), "plain package should be included, got: %s", install)
	})

	t.Run("haskell stack.yaml", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "stack.yaml", "resolver: lts-21.0\n")
		install, test, _ := detectCommands(dir, stackHaskell)
		assert.Equal(t, install, "stack build --only-dependencies --test")
		assert.Equal(t, test, "stack test")
	})

	t.Run("haskell cabal", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "cabal.project", "packages: .\n")
		install, test, _ := detectCommands(dir, stackHaskell)
		assert.Assert(t, strings.Contains(install, "cabal"), "expected cabal in install, got: %s", install)
		assert.Equal(t, test, "cabal test all")
	})

	t.Run("scala plain", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "build.sbt", "name := \"myapp\"\n")
		install, test, _ := detectCommands(dir, stackScala)
		assert.Equal(t, install, "sbt update")
		assert.Equal(t, test, "sbt test")
	})

	t.Run("scala cross-platform (JSPlatform)", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "build.sbt", "lazy val foo = crossProject(JSPlatform, JVMPlatform)\n")
		_, test, _ := detectCommands(dir, stackScala)
		assert.Equal(t, test, "sbt rootJVM/test")
	})

	t.Run("cpp cmake", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "CMakeLists.txt", "cmake_minimum_required(VERSION 3.20)\n")
		install, test, _ := detectCommands(dir, stackCPP)
		assert.Assert(t, strings.Contains(install, "cmake"), "expected cmake in install, got: %s", install)
		assert.Assert(t, strings.Contains(test, "ctest"), "expected ctest in test, got: %s", test)
	})

	t.Run("cpp meson (no cmake)", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "meson.build", "project('myapp', 'cpp')\n")
		install, test, _ := detectCommands(dir, stackCPP)
		assert.Assert(t, strings.Contains(install, "meson"), "expected meson in install, got: %s", install)
		assert.Equal(t, test, "meson test -C build")
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
		assertContains(t, content, "RUN --mount=type=secret,id=netrc,required=false,mode=0444 GONOSUMDB=${GONOSUMDB:-*} GOAUTH=netrc:/run/secrets/netrc go mod download")
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

	t.Run("js/ts stack sets CI=true and uses npmrc secret mount", func(t *testing.T) {
		t.Parallel()
		env := &Environment{
			Stack:        stackTypeScript,
			Install:      "pnpm install",
			Test:         "pnpm test",
			SystemDeps:   []string{"node", "pnpm"},
			Image:        "cimg/node",
			ImageVersion: "22.0",
		}
		content := dockerfileContent(t.TempDir(), env)
		assertContains(t, content, "ENV CI=true")
		assertContains(t, content, "--mount=type=secret,id=npmrc,required=false,mode=0444 NPM_CONFIG_USERCONFIG=/run/secrets/npmrc pnpm install")
	})

	t.Run("python stack uses netrc secret mount", func(t *testing.T) {
		t.Parallel()
		env := &Environment{
			Stack:        stackPython,
			Install:      "pip install -r requirements.txt",
			Test:         "pytest",
			Image:        "cimg/python",
			ImageVersion: "3.12.0",
		}
		content := dockerfileContent(t.TempDir(), env)
		assertContains(t, content, "--mount=type=secret,id=netrc,required=false,mode=0444 NETRC=/run/secrets/netrc pip install -r requirements.txt")
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

func TestRustMemberRequiresNightly(t *testing.T) {
	t.Parallel()

	t.Run("no feature attribute", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "src/lib.rs", "pub fn add(a: i32, b: i32) -> i32 { a + b }\n")
		assert.Assert(t, !rustMemberRequiresNightly(dir))
	})

	t.Run("nightly feature attribute", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "src/lib.rs", "#![feature(async_closure)]\npub fn foo() {}\n")
		assert.Assert(t, rustMemberRequiresNightly(dir))
	})

	t.Run("empty dir", func(t *testing.T) {
		t.Parallel()
		assert.Assert(t, !rustMemberRequiresNightly(t.TempDir()))
	})
}

func TestDetectElixirVersionFromCI(t *testing.T) {
	t.Parallel()

	t.Run("no workflows dir", func(t *testing.T) {
		t.Parallel()
		maj, minor, otp := detectElixirVersionFromCI(t.TempDir())
		assert.Equal(t, maj, 0)
		assert.Equal(t, minor, 0)
		assert.Equal(t, otp, 0)
	})

	t.Run("explicit elixir and otp versions", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, ".github/workflows/ci.yml", `
jobs:
  test:
    strategy:
      matrix:
        elixir: ['1.18', '1.17']
        otp: ['26']
`)
		maj, minor, otp := detectElixirVersionFromCI(dir)
		assert.Equal(t, maj, 1)
		assert.Equal(t, minor, 18)
		assert.Equal(t, otp, 26)
	})

	t.Run("otp latest only returns 0", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, ".github/workflows/ci.yml", `
jobs:
  test:
    strategy:
      matrix:
        elixir: ['1.18']
        otp: ['latest']
`)
		_, _, otp := detectElixirVersionFromCI(dir)
		assert.Equal(t, otp, 0)
	})

	t.Run("mixed otp explicit and latest picks lowest explicit", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, ".github/workflows/ci.yml", `
jobs:
  test:
    strategy:
      matrix:
        elixir: ['1.18']
        otp: ['26', 'latest']
`)
		_, _, otp := detectElixirVersionFromCI(dir)
		assert.Equal(t, otp, 26)
	})

	t.Run("no elixir version returns zeros", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, ".github/workflows/ci.yml", `
jobs:
  test:
    steps:
      - uses: actions/checkout@v4
`)
		maj, minor, otp := detectElixirVersionFromCI(dir)
		assert.Equal(t, maj, 0)
		assert.Equal(t, minor, 0)
		assert.Equal(t, otp, 0)
	})
}

func TestDetectGradleToolchainJDKs(t *testing.T) {
	t.Parallel()

	t.Run("no gradle files", func(t *testing.T) {
		t.Parallel()
		deps := detectGradleToolchainJDKs(t.TempDir())
		assert.Equal(t, len(deps), 0)
	})

	t.Run("gradle.properties with toolchain version", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "gradle.properties", "java.mainToolchainVersion=8\n")
		deps := detectGradleToolchainJDKs(dir)
		assert.Equal(t, len(deps), 1)
		assert.Equal(t, deps[0], "openjdk-8")
	})

	t.Run("build.gradle.kts with JavaLanguageVersion.of", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "build.gradle.kts", `
kotlin {
    jvmToolchain {
        languageVersion.set(JavaLanguageVersion.of(11))
    }
}
`)
		deps := detectGradleToolchainJDKs(dir)
		assert.Equal(t, len(deps), 1)
		assert.Equal(t, deps[0], "openjdk-11")
	})

	t.Run("libs.versions.toml with java version", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "gradle/libs.versions.toml", `
[versions]
java = "8"
kotlin = "1.9.22"
`)
		deps := detectGradleToolchainJDKs(dir)
		assert.Equal(t, len(deps), 1)
		assert.Equal(t, deps[0], "openjdk-8")
	})

	t.Run("only non-LTS versions not mapped", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		// Version 21 is not in the LTS mapping (only 8, 11, 17 are)
		writeFile(t, dir, "gradle.properties", "java.toolchainVersion=21\n")
		deps := detectGradleToolchainJDKs(dir)
		assert.Equal(t, len(deps), 0)
	})
}

func TestDetectDotNetVersion(t *testing.T) {
	t.Parallel()

	t.Run("global.json sdk version", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "global.json", `{"sdk":{"version":"8.0.100"}}`)
		assert.Equal(t, detectDotNetVersion(dir), "8.0")
	})

	t.Run("csproj TargetFramework net9", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "MyApp.csproj", `<Project><PropertyGroup><TargetFramework>net9.0</TargetFramework></PropertyGroup></Project>`)
		assert.Equal(t, detectDotNetVersion(dir), "9.0")
	})

	t.Run("default 8.0 when nothing found", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, detectDotNetVersion(t.TempDir()), "8.0")
	})

	t.Run("netstandard not counted", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "MyLib.csproj", `<Project><PropertyGroup><TargetFramework>netstandard2.0</TargetFramework></PropertyGroup></Project>`)
		assert.Equal(t, detectDotNetVersion(dir), "8.0")
	})
}

func TestDetectDotNetTestFramework(t *testing.T) {
	t.Parallel()

	t.Run("no csproj returns empty", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, detectDotNetTestFramework(t.TempDir()), "")
	})

	t.Run("csproj without test sdk returns empty", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "MyLib.csproj", `<Project><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`)
		assert.Equal(t, detectDotNetTestFramework(dir), "")
	})

	t.Run("test csproj with net8.0", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "MyTests.csproj", `<Project>
  <ItemGroup>
    <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.0.0" />
  </ItemGroup>
  <PropertyGroup>
    <TargetFrameworks>net8.0;net6.0</TargetFrameworks>
  </PropertyGroup>
</Project>`)
		assert.Equal(t, detectDotNetTestFramework(dir), "net8.0")
	})

	t.Run("multiple test projects picks highest", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "Tests1.csproj", `<Project>
  <ItemGroup><PackageReference Include="Microsoft.NET.Test.Sdk" /></ItemGroup>
  <PropertyGroup><TargetFramework>net6.0</TargetFramework></PropertyGroup>
</Project>`)
		writeFile(t, dir, "Tests2.csproj", `<Project>
  <ItemGroup><PackageReference Include="Microsoft.NET.Test.Sdk" /></ItemGroup>
  <PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup>
</Project>`)
		assert.Equal(t, detectDotNetTestFramework(dir), "net8.0")
	})
}

func TestDetectSBTTestCommand(t *testing.T) {
	t.Parallel()

	t.Run("no build.sbt returns sbt test", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, detectSBTTestCommand(t.TempDir()), "sbt test")
	})

	t.Run("plain build.sbt returns sbt test", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "build.sbt", "name := \"myapp\"\nscalaVersion := \"3.3.0\"\n")
		assert.Equal(t, detectSBTTestCommand(dir), "sbt test")
	})

	t.Run("JSPlatform in build.sbt returns rootJVM", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "build.sbt", "lazy val core = crossProject(JSPlatform, JVMPlatform)\n")
		assert.Equal(t, detectSBTTestCommand(dir), "sbt rootJVM/test")
	})

	t.Run("NativePlatform in build.sbt returns rootJVM", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "build.sbt", "lazy val core = crossProject(NativePlatform, JVMPlatform)\n")
		assert.Equal(t, detectSBTTestCommand(dir), "sbt rootJVM/test")
	})

	t.Run("sbt-scalajs in plugins.sbt returns rootJVM", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "build.sbt", "name := \"myapp\"\n")
		writeFile(t, dir, "project/plugins.sbt", "addSbtPlugin(\"org.scala-js\" % \"sbt-scalajs\" % \"1.15.0\")\n")
		assert.Equal(t, detectSBTTestCommand(dir), "sbt rootJVM/test")
	})
}

func TestDetectHaskellGHCVersionFromCI(t *testing.T) {
	t.Parallel()

	t.Run("no workflows dir", func(t *testing.T) {
		t.Parallel()
		maj, minor := detectHaskellGHCVersionFromCI(t.TempDir())
		assert.Equal(t, maj, 0)
		assert.Equal(t, minor, 0)
	})

	t.Run("ghc version in matrix", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, ".github/workflows/ci.yml", `
jobs:
  test:
    strategy:
      matrix:
        ghc: ['9.10', '9.8']
`)
		maj, minor := detectHaskellGHCVersionFromCI(dir)
		assert.Equal(t, maj, 9)
		assert.Equal(t, minor, 10)
	})

	t.Run("ghc-version key", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, ".github/workflows/ci.yml", `
jobs:
  test:
    steps:
      - uses: haskell-actions/setup@v2
        with:
          ghc-version: '9.10.1'
`)
		maj, minor := detectHaskellGHCVersionFromCI(dir)
		assert.Equal(t, maj, 9)
		assert.Equal(t, minor, 10)
	})

	t.Run("picks highest version", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, ".github/workflows/ci.yml", `
jobs:
  test:
    strategy:
      matrix:
        ghc: ['9.6', '9.8', '9.10']
`)
		maj, minor := detectHaskellGHCVersionFromCI(dir)
		assert.Equal(t, maj, 9)
		assert.Equal(t, minor, 10)
	})

	t.Run("no ghc lines returns zeros", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, ".github/workflows/ci.yml", `
jobs:
  build:
    runs-on: ubuntu-22.04
`)
		maj, minor := detectHaskellGHCVersionFromCI(dir)
		assert.Equal(t, maj, 0)
		assert.Equal(t, minor, 0)
	})
}

func TestDetectDartPackages(t *testing.T) {
	t.Parallel()

	t.Run("no packages dirs", func(t *testing.T) {
		t.Parallel()
		pkgs := detectDartPackages(t.TempDir())
		assert.Equal(t, len(pkgs), 0)
	})

	t.Run("pkgs directory with packages", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pkgs/foo/pubspec.yaml", "name: foo\n")
		writeFile(t, dir, "pkgs/bar/pubspec.yaml", "name: bar\n")
		pkgs := detectDartPackages(dir)
		assert.Equal(t, len(pkgs), 2)
	})

	t.Run("packages directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "packages/alpha/pubspec.yaml", "name: alpha\n")
		pkgs := detectDartPackages(dir)
		assert.Equal(t, len(pkgs), 1)
		assert.Equal(t, pkgs[0], "packages/alpha")
	})

	t.Run("flutter packages excluded", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pkgs/plain/pubspec.yaml", "name: plain\n")
		writeFile(t, dir, "pkgs/flutter_ui/pubspec.yaml", "name: flutter_ui\ndependencies:\n  flutter:\n    sdk: flutter\n")
		pkgs := detectDartPackages(dir)
		assert.Equal(t, len(pkgs), 1)
		assert.Equal(t, pkgs[0], "pkgs/plain")
	})

	t.Run("native toolchain packages excluded", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "pkgs/native_pkg/pubspec.yaml", "name: native_pkg\ndependencies:\n  native_toolchain_c: ^0.3.0\n")
		pkgs := detectDartPackages(dir)
		assert.Equal(t, len(pkgs), 0)
	})

	t.Run("dir without pubspec.yaml skipped", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		assert.NilError(t, os.MkdirAll(filepath.Join(dir, "pkgs", "no_pubspec"), 0755))
		pkgs := detectDartPackages(dir)
		assert.Equal(t, len(pkgs), 0)
	})
}

// --- helpers ---

func assertContains(t *testing.T, content, substr string) {
	t.Helper()
	assert.Assert(t, strings.Contains(content, substr), "content does not contain %q\ncontent:\n%s", substr, content)
}
