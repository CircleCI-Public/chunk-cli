package factory

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// ValidationCommands picks the configured commands a build is checked with:
// those that check rather than fix, can run remotely, and need no template
// expanded against the developer's local changes.
func ValidationCommands(cmds []config.Command) []config.Command {
	var out []config.Command
	for _, c := range cmds {
		if c.Role == config.RoleAutofix || c.RunsLocally() || strings.Contains(c.Run, "{{") {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ErrNothingToCheck means a project has neither review prompts nor validation
// commands, so nothing would check a build's work.
var ErrNothingToCheck = errors.New("nothing to check the implementer's work with")

// LoadChecks loads what a build's work is checked with: the review prompts in
// dir and, unless noValidate, cfg's validation commands. With optional, a
// missing or empty dir means no reviews rather than an error, so validation
// commands alone may do.
func LoadChecks(dir string, optional bool, cfg *config.ProjectConfig, noValidate bool) ([]review.Prompt, []config.Command, error) {
	prompts, err := review.LoadPrompts(dir)
	switch {
	case err == nil:
	case optional && (errors.Is(err, review.ErrNoPrompts) || errors.Is(err, os.ErrNotExist)):
	case errors.Is(err, review.ErrNoPrompts):
		return nil, nil, fmt.Errorf("no review prompts in %s: %w", dir, err)
	default:
		return nil, nil, fmt.Errorf("read review prompts from %s: %w", dir, err)
	}
	var commands []config.Command
	if !noValidate {
		commands = ValidationCommands(cfg.Commands)
	}
	if len(prompts) == 0 && len(commands) == 0 {
		return nil, nil, ErrNothingToCheck
	}
	return prompts, commands, nil
}
