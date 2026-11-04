package bootstrap

import (
	"context"
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

// The detection steps chunk init and bootstrap share, each reporting what it
// did through status.

// DetectVCS finds the org and repo of the project's git origin. When there is
// none to find it says why and returns nil; only a cancelled ctx is an error.
func DetectVCS(ctx context.Context, dir string, status iostream.StatusFunc) (*config.VCSConfig, error) {
	org, repo, err := gitremote.DetectOrgAndRepoCtx(ctx, dir)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("detect VCS info: %w", ctxErr)
		}
		status(iostream.LevelWarn, fmt.Sprintf("Could not detect VCS info: %v", err))
		return nil, nil
	}
	status(iostream.LevelStep, "Detected repository: "+ui.Bold(org+"/"+repo))
	return &config.VCSConfig{Org: org, Repo: repo}, nil
}

// DetectCommands detects the project's validation commands, with an install
// command first when one can be found. ok is false when detection failed,
// having said why.
func DetectCommands(ctx context.Context, claude *anthropic.Client, dir string, status iostream.StatusFunc) (commands []config.Command, ok bool) {
	det, err := validate.DetectCommands(ctx, claude, dir)
	if err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not detect commands: %v", err))
		return nil, false
	}
	ReportDetectionSource(det, status)
	commands, pm := validate.WithInstallCommand(dir, det.Commands)
	if pm != nil {
		status(iostream.LevelStep, "Detected package manager: "+pm.Name)
	}
	for _, c := range det.Commands {
		status(iostream.LevelStep, fmt.Sprintf("Detected command: %s (%s)", c.Name, c.Run))
	}
	return commands, true
}

// ReportDetectionSource says where the detected commands came from, and what
// detection could not read. Commands lifted from a CircleCI config can look
// nothing like the toolchain defaults, so the provenance is the difference
// between a surprising config and an explicable one.
func ReportDetectionSource(det validate.Detection, status iostream.StatusFunc) {
	if det.Source != "" {
		status(iostream.LevelStep, "Detected commands from "+ui.Bold(det.Source))
	}
	// With nothing detected, the notes are all detection has to say — and they
	// are what explains a CircleCI config that exists but could not be used.
	// They sit under the source line when there is one; otherwise they are
	// flush left, but dimmed.
	for _, note := range det.Notes {
		if det.Source == "" {
			status(iostream.LevelStep, ui.Dim(note))
			continue
		}
		status(iostream.LevelInfo, note)
	}
}

// UpdateGitignore keeps chunk's machine-local state out of git. It is
// housekeeping, so a failure is reported rather than returned.
func UpdateGitignore(dir string, status iostream.StatusFunc) {
	added, err := config.EnsureGitignore(dir)
	if err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not update .gitignore: %v", err))
		return
	}
	if added {
		status(iostream.LevelDone, "Updated .gitignore with local pool state patterns")
		status(iostream.LevelInfo, "Machine-local sidecar pool IDs; not for commit. Keeps .chunk/config.json tracked.")
	}
}
