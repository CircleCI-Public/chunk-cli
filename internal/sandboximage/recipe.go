// Package sandboximage builds the Docker image that factory runs execute in.
//
// A Recipe is derived deterministically from what smelt detects about the repo
// and what its CI config says; Render turns it into a Dockerfile.smelt that is
// committed to the repo; Builder builds it and Tag says whether the result is
// still fresh.
package sandboximage

import (
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/smeltdev/smelt/internal/ciconfig"
	"github.com/smeltdev/smelt/internal/envspec"
)

// Where a Recipe's base image came from.
const (
	BaseFromCI       = "circleci config"
	BaseFromDetected = "detected"
	BaseFromFallback = "fallback"
)

// UnknownStack is envspec's stack name for a repo nothing was detected in.
const UnknownStack = "unknown"

// Names repeated across the strategy table.
const (
	stackJS      = "javascript"
	stackTS      = "typescript"
	filePackage  = "package.json"
	fileYarnLock = "yarn.lock"
)

// fallbackBase is the base for a repo whose stack is not recognised.
const fallbackBase = "debian:bookworm-slim"

// Recipe is what rendering a Dockerfile.smelt needs.
type Recipe struct {
	Base     string // e.g. "cimg/node:24.21.0"
	BaseFrom string // BaseFromCI, BaseFromDetected or BaseFromFallback
	// User is the base image's user, empty for root. It is read from the image
	// at render time, which is why it is not derived here.
	User      string
	Env       *envspec.Environment
	Manifests []string // repo-relative dependency files, sorted
	Deps      *DepsStrategy
}

// Home is the home directory of the image's final user.
func (r Recipe) Home() string {
	if !r.nonRoot() {
		return "/root"
	}
	return "/home/" + r.User
}

// nonRoot reports whether the base image runs as a user other than root.
func (r Recipe) nonRoot() bool { return r.User != "" && r.User != "root" }

// Unknown reports whether the recipe is the minimal one for an unrecognised stack.
func (r Recipe) Unknown() bool { return r.BaseFrom == BaseFromFallback }

// DepsStrategy is how one package manager's store is warmed at build time.
//
// The store, not the installed project, is baked: the workspace is bind-mounted
// over the repo at run time, which would hide anything installed inside it.
type DepsStrategy struct {
	Name   string
	Stacks []string
	// Require lists root files that must all be tracked for the strategy to apply.
	Require []string
	// Patterns match the base names of the files copied into the scratch directory.
	Patterns []string
	// Prepare runs before Warm, in the scratch directory.
	Prepare string
	// Warm fills the package manager's store.
	Warm string
	// Env pins the store to a path under the home directory; the value may
	// contain the {home} placeholder.
	Env [][2]string
}

// strategies is the one home for per-package-manager knowledge. Order matters:
// the first strategy whose stack and Require files match wins.
var strategies = []DepsStrategy{
	{
		Name: "go", Stacks: []string{"go"},
		Require:  []string{"go.mod"},
		Patterns: []string{"go.mod", "go.sum"},
		Warm:     "go mod download",
	},
	{
		Name: "pnpm", Stacks: []string{stackJS, stackTS},
		Require:  []string{"pnpm-lock.yaml"},
		Patterns: []string{filePackage, "pnpm-lock.yaml", "pnpm-workspace.yaml"},
		Prepare:  "corepack enable --install-directory ~/bin",
		Warm:     "pnpm fetch",
		// pnpm keeps its store on the project's filesystem unless told
		// otherwise; npm_config_store_dir and PNPM_STORE_DIR are ignored.
		Env: [][2]string{{"pnpm_config_store_dir", "{home}/.local/share/pnpm/store"}},
	},
	{
		Name: "yarn-berry", Stacks: []string{stackJS, stackTS},
		Require:  []string{fileYarnLock, ".yarnrc.yml"},
		Patterns: []string{filePackage, fileYarnLock, ".yarnrc.yml"},
		Prepare:  "corepack enable --install-directory ~/bin",
		Warm:     "yarn install --immutable --mode=skip-build",
	},
	{
		Name: "yarn", Stacks: []string{stackJS, stackTS},
		Require:  []string{fileYarnLock},
		Patterns: []string{filePackage, fileYarnLock},
		Warm:     "yarn install --frozen-lockfile --ignore-scripts",
	},
	{
		Name: "npm", Stacks: []string{stackJS, stackTS},
		Require:  []string{"package-lock.json"},
		Patterns: []string{filePackage, "package-lock.json"},
		Warm:     "npm ci --ignore-scripts",
	},
	{
		Name: "uv", Stacks: []string{"python"},
		Require:  []string{"uv.lock"},
		Patterns: []string{"pyproject.toml", "uv.lock"},
		Warm:     "uv sync --frozen --no-install-project",
		// uv falls back to copying, with a warning, across filesystems.
		Env: [][2]string{{"UV_CACHE_DIR", "{home}/.cache/uv"}, {"UV_LINK_MODE", "copy"}},
	},
	{
		Name: "pip", Stacks: []string{"python"},
		Require:  []string{"requirements.txt"},
		Patterns: []string{"requirements*.txt"},
		Warm:     "pip install -r requirements.txt",
	},
	{
		Name: "cargo", Stacks: []string{"rust"},
		Require:  []string{"Cargo.lock"},
		Patterns: []string{"Cargo.toml", "Cargo.lock"},
		// cargo refuses a manifest with no target, and only manifests are copied.
		Warm: `find . -name Cargo.toml -execdir sh -c 'mkdir -p src && touch src/lib.rs' \; && cargo fetch --locked`,
	},
}

// NewRecipe derives a Recipe from detection, the CI config (nil when there is
// none) and the repo's tracked files. It does no I/O.
//
// The CI image wins over detection: matching CI is the point, and detection can
// disagree with it. When it is used the system step is dropped, since the image
// already has its runtime and the step can install a different one over it.
func NewRecipe(env *envspec.Environment, ci *ciconfig.Result, tracked []string) Recipe {
	if env == nil {
		env = &envspec.Environment{Stack: UnknownStack}
	}
	r := Recipe{Env: env}
	switch {
	case ci != nil && ci.CommonImage() != "":
		r.Base, r.BaseFrom = ci.CommonImage(), BaseFromCI
		withoutSystem := *env
		withoutSystem.Setup = slices.DeleteFunc(slices.Clone(env.Setup), func(s envspec.Step) bool { return s.Name == "system" })
		r.Env = &withoutSystem
	case env.Stack == UnknownStack || env.Image == "":
		r.Base, r.BaseFrom = fallbackBase, BaseFromFallback
		return r
	default:
		r.Base, r.BaseFrom = env.Image+":"+env.ImageVersion, BaseFromDetected
	}
	r.Deps = pickStrategy(env.Stack, tracked)
	if r.Deps != nil {
		r.Manifests = manifests(*r.Deps, tracked)
	}
	return r
}

func pickStrategy(stack string, tracked []string) *DepsStrategy {
	for i := range strategies {
		s := &strategies[i]
		if !slices.Contains(s.Stacks, stack) {
			continue
		}
		if slices.ContainsFunc(s.Require, func(f string) bool { return !slices.Contains(tracked, f) }) {
			continue
		}
		return s
	}
	return nil
}

// manifests returns the tracked files the strategy copies, sorted.
func manifests(s DepsStrategy, tracked []string) []string {
	var out []string
	for _, f := range tracked {
		for _, p := range s.Patterns {
			if ok, _ := path.Match(p, path.Base(f)); ok && !strings.HasPrefix(f, "../") {
				out = append(out, f)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
