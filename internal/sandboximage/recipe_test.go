package sandboximage

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/ciconfig"
	"github.com/smeltdev/smelt/internal/envspec"
)

func jsEnv() *envspec.Environment {
	return &envspec.Environment{
		Stack: "typescript", Image: "cimg/node", ImageVersion: "22.12",
		Setup: []envspec.Step{{Name: "system", Command: "sudo npm install -g pnpm"}, {Name: "install", Command: "pnpm install"}},
	}
}

func TestNewRecipe(t *testing.T) {
	ci := &ciconfig.Result{Images: []ciconfig.JobImage{{Job: "test", Image: "cimg/node:24.21.0"}}}
	mixed := &ciconfig.Result{Images: []ciconfig.JobImage{{Job: "a", Image: "x:1"}, {Job: "b", Image: "x:2"}}}
	pnpmFiles := []string{"package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "packages/a/package.json", "src/index.ts"}

	tests := []struct {
		name          string
		env           *envspec.Environment
		ci            *ciconfig.Result
		tracked       []string
		wantBase      string
		wantFrom      string
		wantDeps      string
		wantManifests []string
		wantSystem    bool
	}{
		{"ci image wins and drops system step", jsEnv(), ci, pnpmFiles, "cimg/node:24.21.0", BaseFromCI, "pnpm",
			[]string{"package.json", "packages/a/package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml"}, false},
		{"mixed ci images fall back to detection", jsEnv(), mixed, pnpmFiles, "cimg/node:22.12", BaseFromDetected, "pnpm",
			[]string{"package.json", "packages/a/package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml"}, true},
		{"no ci config", jsEnv(), nil, []string{"package.json", "package-lock.json"}, "cimg/node:22.12", BaseFromDetected, "npm",
			[]string{"package-lock.json", "package.json"}, true},
		{"yarn berry", jsEnv(), nil, []string{"package.json", "yarn.lock", ".yarnrc.yml"}, "cimg/node:22.12", BaseFromDetected, "yarn-berry",
			[]string{".yarnrc.yml", "package.json", "yarn.lock"}, true},
		{"yarn classic", jsEnv(), nil, []string{"package.json", "yarn.lock"}, "cimg/node:22.12", BaseFromDetected, "yarn",
			[]string{"package.json", "yarn.lock"}, true},
		{"no lockfile means no dependency layer", jsEnv(), nil, []string{"package.json"}, "cimg/node:22.12", BaseFromDetected, "", nil, true},
		{"go", &envspec.Environment{Stack: "go", Image: "cimg/go", ImageVersion: "1.24"}, nil, []string{"go.mod", "go.sum", "main.go"},
			"cimg/go:1.24", BaseFromDetected, "go", []string{"go.mod", "go.sum"}, false},
		{"uv beats pip", &envspec.Environment{Stack: "python", Image: "cimg/python", ImageVersion: "3.12"}, nil,
			[]string{"pyproject.toml", "uv.lock", "requirements.txt"}, "cimg/python:3.12", BaseFromDetected, "uv",
			[]string{"pyproject.toml", "uv.lock"}, false},
		{"pip", &envspec.Environment{Stack: "python", Image: "cimg/python", ImageVersion: "3.12"}, nil,
			[]string{"requirements.txt", "requirements-dev.txt"}, "cimg/python:3.12", BaseFromDetected, "pip",
			[]string{"requirements-dev.txt", "requirements.txt"}, false},
		{"rust workspace", &envspec.Environment{Stack: "rust", Image: "cimg/rust", ImageVersion: "1.80"}, nil,
			[]string{"Cargo.toml", "Cargo.lock", "crates/a/Cargo.toml"}, "cimg/rust:1.80", BaseFromDetected, "cargo",
			[]string{"Cargo.lock", "Cargo.toml", "crates/a/Cargo.toml"}, false},
		{"unknown stack", &envspec.Environment{Stack: "unknown"}, nil, []string{"Makefile"}, "debian:bookworm-slim", BaseFromFallback, "", nil, false},
		{"unknown stack ignores nothing in ci", &envspec.Environment{Stack: "unknown"}, ci, nil, "cimg/node:24.21.0", BaseFromCI, "", nil, false},
		{"nil env", nil, nil, nil, "debian:bookworm-slim", BaseFromFallback, "", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRecipe(tt.env, tt.ci, tt.tracked)
			assert.Equal(t, r.Base, tt.wantBase)
			assert.Equal(t, r.BaseFrom, tt.wantFrom)
			got := ""
			if r.Deps != nil {
				got = r.Deps.Name
			}
			assert.Equal(t, got, tt.wantDeps)
			assert.DeepEqual(t, r.Manifests, tt.wantManifests)
			assert.Equal(t, r.Env.SetupStep("system") != "", tt.wantSystem && tt.env != nil && tt.env.SetupStep("system") != "")
		})
	}
}
