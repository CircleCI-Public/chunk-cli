package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/codexauth"
	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/all"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/sandboximage"
	"github.com/smeltdev/smelt/internal/ui"
)

// harnessAction is what choosing the run's harness comes down to.
type harnessAction int

const (
	harnessUse  harnessAction = iota // the name is decided
	harnessAsk                       // ask the user
	harnessFail                      // nothing says, and nobody can be asked
)

// harnessInputs are the facts the choice of harness is made from.
type harnessInputs struct {
	// Flag is --harness and Config is factory.harness; either may be empty.
	Flag, Config string
	// CanPrompt reports that a person can be asked.
	CanPrompt bool
}

// decideHarness applies the precedence: --harness, then factory.harness, then
// asking. With nobody to ask there is no default: choosing silently would spend
// somebody's subscription.
func decideHarness(in harnessInputs) (string, harnessAction) {
	switch {
	case in.Flag != "":
		return in.Flag, harnessUse
	case in.Config != "":
		return in.Config, harnessUse
	case in.CanPrompt:
		return "", harnessAsk
	}
	return "", harnessFail
}

// harnessNames lists the harnesses, for messages.
func harnessNames() []string {
	names := make([]string, 0, len(all.All()))
	for _, h := range all.All() {
		names = append(names, string(h.Name()))
	}
	return names
}

// harnessReady reports whether name has a credential on this machine.
func harnessReady(name harness.Name) bool {
	if name == harness.Codex {
		_, err := codexauth.Find()
		return err == nil
	}
	rc, err := config.Resolve("", "", false)
	if err != nil {
		return false
	}
	_, _, err = reviewCredential(rc)
	return err == nil
}

// harnessChoice is everything resolveHarness needs.
type harnessChoice struct {
	Root string
	Cfg  *config.ProjectConfig
	Flag string
	// Image is the Docker image the run will use, "" when not known.
	Image     string
	CanPrompt bool
}

// resolveHarness picks the coding agent for a run. An image that carries
// agent labels and lacks the chosen agent is refused before anything starts.
func resolveHarness(ctx context.Context, streams iostream.Streams, c harnessChoice) (string, error) {
	var fromConfig string
	if c.Cfg != nil && c.Cfg.Factory != nil {
		fromConfig = c.Cfg.Factory.Harness
	}
	if !config.ValidHarness(c.Flag) {
		return "", unknownHarnessError(c.Flag)
	}
	inImage := imageHarnesses(ctx, c.Image)
	name, action := decideHarness(harnessInputs{Flag: c.Flag, Config: fromConfig, CanPrompt: c.CanPrompt})
	switch action {
	case harnessUse:
	case harnessFail:
		return "", newUserError("No coding agent is chosen for this run.").
			withCode("command.invalid_args").
			withSuggestion(fmt.Sprintf("Pass --harness (%s), or set factory.harness in .smelt/config.json.", strings.Join(harnessNames(), " or "))).
			withExitCode(ExitBadArgs).
			withoutDetail()
	case harnessAsk:
		var err error
		if name, err = askHarness(streams, inImage); err != nil {
			return "", err
		}
		offerSaveHarness(streams, c.Root, c.Cfg, name)
	}
	if len(inImage) > 0 && !slices.Contains(inImage, name) {
		return "", newUserError(fmt.Sprintf("The sandbox image does not have %s.", name)).
			withCode("factory.harness_not_in_image").
			withSuggestion("Rebuild it with the current agent kit: smelt factory image --regenerate").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return name, nil
}

func unknownHarnessError(name string) error {
	return newUserError(fmt.Sprintf("Unknown harness %q.", name)).
		withCode("command.invalid_args").
		withSuggestion("Choose one of: " + strings.Join(harnessNames(), ", ") + ".").
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// askHarness asks which agent should implement and review this run, marking
// the ones that are logged in and in the image.
func askHarness(streams iostream.Streams, inImage []string) (string, error) {
	items := make([]string, 0, len(all.All()))
	for _, h := range all.All() {
		var marks []string
		if harnessReady(h.Name()) {
			marks = append(marks, "logged in")
		}
		if slices.Contains(inImage, string(h.Name())) {
			marks = append(marks, "in the image")
		}
		item := string(h.Name())
		if len(marks) > 0 {
			item += " (" + strings.Join(marks, ", ") + ")"
		}
		items = append(items, item)
	}
	i, err := ui.SelectFromList("Which agent should implement and review this run?", items)
	if err != nil {
		return "", newUserError("No coding agent was chosen.").
			withCode("command.invalid_args").
			withSuggestion("Pass --harness, or set factory.harness in .smelt/config.json.").
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	streams.ErrPrintln("")
	return string(all.All()[i].Name()), nil
}

// offerSaveHarness offers to keep the answer as factory.harness. Declining or
// failing to save costs nothing: the run goes ahead either way.
func offerSaveHarness(streams iostream.Streams, root string, cfg *config.ProjectConfig, name string) {
	if cfg == nil {
		return
	}
	ok, err := confirmPublish("Save "+name+" as factory.harness in .smelt/config.json?", true)
	if err != nil || !ok {
		return
	}
	if cfg.Factory == nil {
		cfg.Factory = &config.FactoryConfig{}
	}
	cfg.Factory.Harness = name
	if err := config.SaveProjectConfig(root, cfg); err != nil {
		streams.ErrPrintln(ui.ErrWarning("Could not save factory.harness: " + err.Error()))
	}
}

// imageHarnesses is the agents the Docker image carries, from its label, or
// nil when that cannot be told: no image, no Docker, an older image.
func imageHarnesses(ctx context.Context, image string) []string {
	if image == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, err := sandboximage.Connect()
	if err != nil {
		return nil
	}
	defer func() { _ = b.Close() }()
	names, _ := b.Harnesses(ctx, image)
	return names
}
