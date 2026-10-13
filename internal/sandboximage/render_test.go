package sandboximage

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	cmp "gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/golden"

	"github.com/smeltdev/smelt/internal/envspec"
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

func TestKitInstallsBothAgentsAndLabelsTheImage(t *testing.T) {
	got := Render(NewRecipe(&envspec.Environment{Stack: "unknown"}, nil, nil))
	assert.Check(t, cmp.Contains(got, KitCodex()))
	assert.Check(t, cmp.Contains(got, "LABEL "+LabelHarnesses+"=claude,codex"))
	assert.Check(t, cmp.Contains(got, "claude.ai/install.sh"))
}

func TestDockerfileLocalCarriesTheKitsCodexBlock(t *testing.T) {
	local, err := os.ReadFile("../../Dockerfile.local")
	assert.NilError(t, err)
	assert.Check(t, cmp.Contains(string(local), KitCodex()))
	assert.Check(t, cmp.Contains(string(local), "LABEL "+LabelHarnesses+"=claude,codex"))
}

func TestKitInstallsTheCodexCommandHost(t *testing.T) {
	got := Render(NewRecipe(&envspec.Environment{Stack: "unknown"}, nil, nil))
	assert.Check(t, cmp.Contains(got, "(v3)"))
	assert.Check(t, cmp.Contains(got, `name="codex-code-mode-host-${arch}-unknown-linux-musl"`))
	assert.Check(t, cmp.Contains(got, `test -x "$HOME/.local/bin/codex-code-mode-host"`))
	assert.Check(t, cmp.Equal(strings.Count(got, "sha256sum -c -"), 2))
}

// The version lives in CodexVersion alone, and each asset has a full SHA-256
// for both architectures.
func TestCodexAssetsArePinnedForBothArchitectures(t *testing.T) {
	sha := regexp.MustCompile(`^[0-9a-f]{64}$`)
	seen := map[string]bool{}
	for _, a := range []codexAsset{codexMain, codexHost} {
		for _, sum := range []string{a.sumX86, a.sumARM} {
			assert.Check(t, sha.MatchString(sum), "%s: %q", a.prefix, sum)
			assert.Check(t, !seen[sum], "duplicate checksum %s", sum)
			seen[sum] = true
			assert.Check(t, cmp.Contains(a.install(), sum))
		}
		assert.Check(t, cmp.Contains(a.install(), codexReleaseURLBase+CodexVersion+"/"))
	}
	assert.Check(t, cmp.Len(seen, 4))
}
