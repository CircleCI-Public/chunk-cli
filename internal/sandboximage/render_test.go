package sandboximage

import (
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/golden"

	"github.com/CircleCI-Public/chunk-cli/internal/envspec"
)

func TestRenderGolden(t *testing.T) {
	env := func(stack, image, version string, setup ...envspec.Step) *envspec.Environment {
		return &envspec.Environment{Stack: stack, Image: image, ImageVersion: version, Setup: setup}
	}
	tests := []struct {
		name    string
		env     *envspec.Environment
		tracked []string
		user    string
		base    string // overrides the base, as a CI image would
	}{
		{"go", env("go", "cimg/go", "1.24"), []string{"go.mod", "go.sum"}, "circleci", ""},
		{"pnpm", env("typescript", "cimg/node", "22.12", envspec.Step{Name: "system", Command: "sudo npm install -g pnpm"}),
			[]string{"package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "packages/a/package.json"}, "circleci", "cimg/node:24.21.0"},
		{"pnpm-detected", env("typescript", "cimg/node", "22.12", envspec.Step{Name: "system", Command: "sudo npm install -g pnpm"}),
			[]string{"package.json", "pnpm-lock.yaml"}, "circleci", ""},
		{"npm", env("javascript", "cimg/node", "22.12"), []string{"package.json", "package-lock.json"}, "circleci", ""},
		{"uv", env("python", "python", "3.12"), []string{"pyproject.toml", "uv.lock"}, "", ""},
		{"pip", env("python", "cimg/python", "3.12"), []string{"requirements.txt"}, "circleci", ""},
		{"rust", env("rust", "cimg/rust", "1.80"), []string{"Cargo.toml", "Cargo.lock", "crates/a/Cargo.toml"}, "circleci", ""},
		{"unknown", env("unknown", "", ""), nil, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRecipe(tt.env, nil, tt.tracked)
			if tt.base != "" {
				r.Base, r.BaseFrom = tt.base, BaseFromCI
			}
			r.User = tt.user
			got := Render(r)

			golden.Assert(t, got, tt.name+".Dockerfile")

			// What is rendered must be readable by the build context parser.
			_, err := CopySources(got)
			assert.NilError(t, err)
		})
	}
}
