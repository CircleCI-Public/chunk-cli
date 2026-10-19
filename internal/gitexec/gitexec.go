// Package gitexec provides the low-level process boundary for local git commands.
package gitexec

import (
	"context"
	"os/exec"
	"strings"
)

// Runner executes git commands in Dir.
type Runner struct {
	Dir string
	Env []string
}

// Output runs git with args and returns its standard output.
func (r Runner) Output(ctx context.Context, args ...string) ([]byte, error) {
	return r.command(ctx, args...).Output()
}

// CombinedOutput runs git with args and returns its combined standard output
// and standard error.
func (r Runner) CombinedOutput(ctx context.Context, args ...string) ([]byte, error) {
	return r.command(ctx, args...).CombinedOutput()
}

// Run runs git with args.
func (r Runner) Run(ctx context.Context, args ...string) error {
	return r.command(ctx, args...).Run()
}

func (r Runner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	if r.Env != nil {
		cmd.Env = r.Env
	}
	return cmd
}

// PatchFlags make git's diff output a plain patch, whatever the user's git
// configuration says about colour, external diff tools and text conversion.
var PatchFlags = []string{"--no-color", "--no-ext-diff", "--no-textconv"}

// MaxPatchBytes is the most of a patch shown to a reader. Past it, the patch is
// cut, as a lockfile or generated code can make one tens of megabytes.
const MaxPatchBytes = 1 << 20

// CapPatch cuts a patch to MaxPatchBytes at a line boundary, saying so when it
// had to.
func CapPatch(patch string) string {
	if len(patch) <= MaxPatchBytes {
		return patch
	}
	cut := patch[:MaxPatchBytes]
	if i := strings.LastIndexByte(cut, '\n'); i >= 0 {
		cut = cut[:i]
	}
	return cut + "\n… the rest of the diff is too long to show\n"
}
