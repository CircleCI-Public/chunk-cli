package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/sandboximage"
	"github.com/smeltdev/smelt/internal/ui"
)

// kitWarnedFile is the stamp, in the project's data dir, of the day the
// outdated-kit warning was last printed.
const kitWarnedFile = "kit-warned"

// kitNotice says when and how an outdated kit is reported.
type kitNotice struct {
	Root, Dockerfile string
	Now              time.Time
	// EveryTime prints the warning on every call; otherwise at most once a
	// day, so a user who keeps an old kit on purpose is not nagged.
	EveryTime bool
}

// kitWarning is the text to warn with when the committed dockerfile carries an
// older agent kit, "" when it is current, newer or unreadable. It is pure but
// for reading the file.
func kitWarning(root, dockerfile string) string {
	raw, err := os.ReadFile(filepath.Join(root, dockerfile))
	if err != nil {
		return ""
	}
	v, _ := sandboximage.KitVersionOf(string(raw))
	if v >= sandboximage.KitVersion {
		return ""
	}
	return sandboximage.KitOutdatedWarning(dockerfile, v)
}

// noteKitOutdated prints the outdated-kit warning when it is due and returns
// its text, whether or not it was printed, so the run can record it. It never
// fails a run.
func noteKitOutdated(streams iostream.Streams, n kitNotice) string {
	warning := kitWarning(n.Root, n.Dockerfile)
	if warning == "" {
		return ""
	}
	today := n.Now.Format(time.DateOnly)
	stamp := kitStampPath(n.Root)
	if !n.EveryTime && stamp != "" {
		if raw, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(raw)) == today {
			return warning
		}
	}
	streams.ErrPrintln(ui.ErrWarning(warning))
	if stamp != "" && os.MkdirAll(filepath.Dir(stamp), 0o700) == nil {
		_ = os.WriteFile(stamp, []byte(today+"\n"), 0o644)
	}
	return warning
}

func kitStampPath(root string) string {
	dir, err := config.ProjectDataDir(root)
	if err != nil {
		return ""
	}
	return filepath.Join(dir, kitWarnedFile)
}

// kitUpdate is what --update-kit works on.
type kitUpdate struct {
	Root, Dockerfile string
	// Yes writes without asking.
	Yes bool
}

// updateKit replaces the agent-kit section of the Dockerfile with the current
// one, shows the diff and asks before writing. Everything above the section is
// kept as it is.
func updateKit(ctx context.Context, streams iostream.Streams, u kitUpdate) error {
	path := filepath.Join(u.Root, u.Dockerfile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return newUserError("Could not read " + u.Dockerfile + ".").
			withCode("factory.image_dockerfile").
			withSuggestion("Run smelt factory image to generate it.").
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	current := string(raw)
	updated, err := sandboximage.UpdateKit(current)
	if errors.Is(err, sandboximage.ErrNoKit) {
		return newUserError(u.Dockerfile + " has no smelt agent kit section, so its kit cannot be updated in place.").
			withCode("command.invalid_args").
			withSuggestion("Run smelt factory image --regenerate, or add the agent kit by hand (see the --update-kit entry in docs/CLI.md).").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	if err != nil {
		return &userError{msg: "Could not update the agent kit.", err: err}
	}
	if updated == current {
		streams.ErrPrintln(u.Dockerfile + " already has the current agent kit.")
		return nil
	}
	diff, err := sandboximage.DiffText(ctx, u.Dockerfile, current, updated)
	if err != nil {
		return &userError{msg: "Could not compare " + u.Dockerfile + ".", err: err}
	}
	streams.ErrPrintln(diff)
	if !u.Yes {
		if err := confirmKitUpdate(u.Dockerfile); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return &userError{msg: "Could not write " + u.Dockerfile + ".", err: err}
	}
	streams.ErrPrintln(ui.Success("Updated the agent kit in " + u.Dockerfile))
	return nil
}

func confirmKitUpdate(dockerfile string) error {
	if !isInteractive() {
		return newUserError("Updating the agent kit needs confirmation.").
			withCode("command.invalid_args").
			withSuggestion("Run smelt factory image --update-kit in a terminal, or pass --yes.").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	ok, err := ui.Confirm("Update the agent kit in "+dockerfile+"?", false)
	if err != nil || !ok {
		return newUserError(dockerfile + " left as it was.").withCode("command.cancelled").withExitCode(ExitGeneral).withoutDetail()
	}
	return nil
}
