package validate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestDetectCommands(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, dir string)
		wantRuns  []string
		wantRoles []string
	}{
		{
			name: "taskfile + go",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "Taskfile.yml", "version: '3'\n")
				writeFile(t, dir, "go.mod", "module example.com/test\n\ngo 1.22\n")
			},
			wantRuns:  []string{"task test", "task test -- {{CHANGED_PACKAGES}}", "task lint", "task fmt"},
			wantRoles: []string{config.RoleGate, config.RolePrecheck, config.RoleGate, config.RoleAutofix},
		},
		{
			name: "taskfile only",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "Taskfile.yml", "version: '3'\n")
			},
			wantRuns:  []string{"task test"},
			wantRoles: []string{config.RoleGate},
		},
		{
			name: "taskfile yaml extension",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "Taskfile.yaml", "version: '3'\n")
			},
			wantRuns:  []string{"task test"},
			wantRoles: []string{config.RoleGate},
		},
		{
			name: "makefile + go",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "Makefile", "test:\n\tgo test ./...\n")
				writeFile(t, dir, "go.mod", "module example.com/test\n\ngo 1.22\n")
			},
			wantRuns:  []string{"make test", "make lint"},
			wantRoles: []string{config.RoleGate, config.RoleGate},
		},
		{
			name: "makefile only",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "Makefile", "test:\n\techo ok\n")
			},
			wantRuns:  []string{"make test"},
			wantRoles: []string{config.RoleGate},
		},
		{
			name: "go only",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "go.mod", "module example.com/test\n\ngo 1.22\n")
			},
			wantRuns:  []string{"go test ./...", "golangci-lint run ./...", "gofmt -w ."},
			wantRoles: []string{config.RoleGate, config.RoleGate, config.RoleAutofix},
		},
		{
			name: "rust",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "Cargo.toml", "[package]\nname = \"test\"\n")
			},
			wantRuns:  []string{"cargo test"},
			wantRoles: []string{config.RoleGate},
		},
		{
			name: "python",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "pyproject.toml", "[tool.pytest.ini_options]\n")
			},
			wantRuns:  []string{"pytest"},
			wantRoles: []string{config.RoleGate},
		},
		{
			name: "node npm",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package.json", `{"name":"test"}`)
				writeFile(t, dir, "package-lock.json", `{"lockfileVersion":3}`)
			},
			wantRuns:  []string{"npm test"},
			wantRoles: []string{config.RoleGate},
		},
		{
			name: "node pnpm",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package.json", `{"name":"test"}`)
				writeFile(t, dir, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
			},
			wantRuns:  []string{"pnpm test"},
			wantRoles: []string{config.RoleGate},
		},
		{
			name: "node yarn",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package.json", `{"name":"test"}`)
				writeFile(t, dir, "yarn.lock", "# yarn lockfile v1\n")
			},
			wantRuns:  []string{"yarn test"},
			wantRoles: []string{config.RoleGate},
		},
		{
			name: "node bun",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package.json", `{"name":"test"}`)
				writeFile(t, dir, "bun.lock", "# bun lockfile\n")
			},
			wantRuns:  []string{"bun test"},
			wantRoles: []string{config.RoleGate},
		},
		{
			name:     "default fallback",
			setup:    func(t *testing.T, dir string) {},
			wantRuns: []string{"npm test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)

			cmds, err := DetectCommands(context.Background(), nil, dir)
			assert.NilError(t, err)
			assert.Equal(t, len(cmds), len(tt.wantRuns),
				"expected %d commands, got %d: %v", len(tt.wantRuns), len(cmds), cmds)

			for i, wantRun := range tt.wantRuns {
				assert.Equal(t, cmds[i].Run, wantRun,
					"command[%d].Run: expected %q, got %q", i, wantRun, cmds[i].Run)
			}
			for i, wantRole := range tt.wantRoles {
				assert.Equal(t, cmds[i].Role, wantRole,
					"command[%d].Role: expected %q, got %q", i, wantRole, cmds[i].Role)
			}
		})
	}
}

func TestDetectCommandsGoFileExt(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/test\n\ngo 1.22\n")

	cmds, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)

	for _, c := range cmds {
		if c.Role != config.RoleAutofix {
			assert.Equal(t, c.FileExt, ".go",
				"command %q: expected FileExt=.go, got %q", c.Run, c.FileExt)
		}
	}
}

func TestDetectPackageManager(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, dir string)
		wantName string
	}{
		{
			name: "pnpm",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
			},
			wantName: "pnpm",
		},
		{
			name: "yarn",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "yarn.lock", "# yarn lockfile v1\n")
			},
			wantName: "yarn",
		},
		{
			name: "bun lockb",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "bun.lockb", "")
			},
			wantName: "bun",
		},
		{
			name: "npm",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package-lock.json", `{"lockfileVersion":3}`)
			},
			wantName: "npm",
		},
		{
			name: "monorepo subdir",
			setup: func(t *testing.T, dir string) {
				// DetectPackageManager searches one level deep; lockfile in a direct subdir.
				subdir := filepath.Join(dir, "packages")
				assert.NilError(t, os.MkdirAll(subdir, 0o755))
				writeFile(t, subdir, "yarn.lock", "# yarn lockfile v1\n")
			},
			wantName: "yarn",
		},
		{
			name:     "none",
			setup:    func(t *testing.T, dir string) {},
			wantName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)

			pm := DetectPackageManager(dir)
			if tt.wantName == "" {
				assert.Assert(t, pm == nil, "expected nil PackageManager, got: %v", pm)
			} else {
				assert.Assert(t, pm != nil, "expected PackageManager %q, got nil", tt.wantName)
				assert.Equal(t, pm.Name, tt.wantName)
			}
		})
	}
}
