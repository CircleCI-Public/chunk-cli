package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// defaultFactoryImage is the Docker image offered by the init factory step.
const defaultFactoryImage = "chunk-sandbox:local"

// initFactoryOptions are the flags that steer the factory step of `chunk init`.
type initFactoryOptions struct {
	// Backend and Image are --factory-backend and --factory-image. Either one
	// given answers the step without a prompt.
	Backend string
	Image   string
	// Author is --factory-author: who factory commits are attributed to, saved
	// as factory.author whatever else the step does.
	Author string
	// Skip is --skip-factory.
	Skip bool
}

// flagsGiven reports whether either flag answered the step.
func (o initFactoryOptions) flagsGiven() bool { return o.Backend != "" || o.Image != "" }

// initFactoryAction is what the factory step of `chunk init` does.
type initFactoryAction int

const (
	factorySkipped      initFactoryAction = iota // nothing to do
	factoryFromFlags                             // save the values the flags gave
	factoryReportExists                          // describe the existing section
	factoryAsk                                   // ask where runs execute
)

// initFactoryState is what the factory step of `chunk init` decides on.
type initFactoryState struct {
	Interactive bool
	FlagsGiven  bool
	Skip        bool
	HasSection  bool
}

// decideInitFactory picks what the factory step does. Flags win in any mode;
// otherwise an existing section is only described, and the question is only
// asked in an interactive session.
func decideInitFactory(s initFactoryState) initFactoryAction {
	switch {
	case s.Skip:
		return factorySkipped
	case s.FlagsGiven:
		return factoryFromFlags
	case s.HasSection:
		return factoryReportExists
	case !s.Interactive:
		return factorySkipped
	}
	return factoryAsk
}

// checkFactoryBackend reports a --factory-backend value that is not a backend.
func checkFactoryBackend(backend string) error {
	if config.ValidBackend(backend) {
		return nil
	}
	return newUserError(fmt.Sprintf("Unknown factory backend %q.", backend)).
		withCode("command.invalid_flags").
		withSuggestion(fmt.Sprintf("Use --factory-backend %s or %s.", config.BackendCircleCI, config.BackendDocker)).
		withExitCode(ExitBadArgs).
		withoutDetail()
}

func describeFactory(f *config.FactoryConfig) string {
	backend := config.BackendOrDefault(f.Backend)
	if f.Image != "" {
		return fmt.Sprintf("Factory runs: %s (%s)", backend, f.Image)
	}
	return "Factory runs: " + backend
}

// check reports a --factory-backend or --factory-author value that init cannot
// save, before anything is written.
func (o initFactoryOptions) check() error {
	if err := checkFactoryBackend(o.Backend); err != nil {
		return err
	}
	return checkFactoryAuthor(o.Author)
}

// checkFactoryAuthor reports a --factory-author value that is not "Name <email>".
func checkFactoryAuthor(author string) error {
	if author == "" {
		return nil
	}
	if _, err := config.ParseCommitAuthor(author); err != nil {
		return newUserError(fmt.Sprintf("Invalid --factory-author: %v.", err)).
			withCode("command.invalid_flags").
			withSuggestion(`Write it as "Name <email>", in quotes.`).
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return nil
}

// initFactoryStep runs the factory step against cfg and reports whether cfg
// changed and needs saving. A cancelled prompt leaves cfg alone.
func initFactoryStep(ctx context.Context, workDir string, streams iostream.Streams, cfg *config.ProjectConfig, opts initFactoryOptions) bool {
	changed := initFactoryBackendStep(ctx, workDir, streams, cfg, opts)
	if opts.Author == "" {
		return changed
	}
	if cfg.Factory == nil {
		cfg.Factory = &config.FactoryConfig{}
	}
	// Validated by checkFactoryAuthor before init started.
	author, _ := config.ParseCommitAuthor(opts.Author)
	cfg.Factory.Author = author.String()
	streams.ErrPrintln(describeAuthor(factory.ResolveAuthor(ctx, workDir, author)))
	return true
}

// initFactoryBackendStep is the factory step's questions: where runs execute
// and, the first time, who their commits are attributed to.
func initFactoryBackendStep(ctx context.Context, workDir string, streams iostream.Streams, cfg *config.ProjectConfig, opts initFactoryOptions) bool {
	switch decideInitFactory(initFactoryState{
		Interactive: isInteractive(),
		FlagsGiven:  opts.flagsGiven(),
		Skip:        opts.Skip,
		HasSection:  cfg.Factory != nil,
	}) {
	case factorySkipped:
		return false
	case factoryReportExists:
		streams.ErrPrintln(describeFactory(cfg.Factory))
		if opts.Author == "" {
			configured, _ := cfg.Factory.CommitAuthor() // validated on load
			streams.ErrPrintln(describeAuthor(factory.ResolveAuthor(ctx, workDir, configured)))
		}
		return false
	case factoryFromFlags:
		backend := opts.Backend
		if backend == "" {
			backend = config.BackendDocker // an image only makes sense for docker
		}
		applyFactory(cfg, streams, backend, opts.Image)
		return true
	case factoryAsk:
		// Handled below: asking is the only action that needs the prompts.
	}

	backend, image, ok := askFactory(streams)
	if !ok {
		return false
	}
	applyFactory(cfg, streams, backend, image)
	if backend == config.BackendDocker {
		if _, err := os.Stat(filepath.Join(workDir, "Dockerfile.local")); err == nil {
			streams.ErrPrintln(ui.Dim(fmt.Sprintf("Build it with: docker build -t %s -f Dockerfile.local .", image)))
		}
	}
	if opts.Author == "" {
		if author, ok := askAuthor(streams, factory.ResolveAuthor(ctx, workDir, config.CommitAuthor{})); ok && author != "" {
			cfg.Factory.Author = author
			streams.ErrPrintln(ui.Dim("Saved factory.author; .chunk/config.json is committed, so everyone here commits as this author."))
		}
	}
	return true
}

// askFactory asks where factory runs execute and, for docker, which image. ok is
// false when the user cancelled or the prompt could not be shown.
func askFactory(streams iostream.Streams) (backend, image string, ok bool) {
	items := []string{"docker (local containers)", "circleci (cloud sidecars)"}
	idx, err := ui.SelectFromList("Where should factory runs execute?", items)
	if err != nil {
		warnPromptError(streams, err)
		return "", "", false
	}
	if idx != 0 {
		return config.BackendCircleCI, "", true
	}
	image, err = ui.PromptText("Docker image for factory runs", defaultFactoryImage)
	if err != nil {
		warnPromptError(streams, err)
		return "", "", false
	}
	if image == "" {
		image = defaultFactoryImage
	}
	return config.BackendDocker, image, true
}

// warnPromptError reports a prompt that failed for a reason other than there
// being no terminal or the user cancelling, which need no explanation.
func warnPromptError(streams iostream.Streams, err error) {
	if errors.Is(err, ui.ErrNoTTY) || errors.Is(err, ui.ErrCancelled) {
		return
	}
	streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not ask about factory runs: %v", err)))
}

// applyFactory records the backend and, for docker, the image, keeping any other
// factory settings already in the config, and says what it recorded.
func applyFactory(cfg *config.ProjectConfig, streams iostream.Streams, backend, image string) {
	if cfg.Factory == nil {
		cfg.Factory = &config.FactoryConfig{}
	}
	cfg.Factory.Backend = backend
	if backend == config.BackendDocker {
		cfg.Factory.Image = image
	}
	streams.ErrPrintln(describeFactory(cfg.Factory))
}

// initFactoryRerun runs the factory step for a project that already has a
// config, saving it when the step changed it.
func initFactoryRerun(ctx context.Context, workDir string, streams iostream.Streams, cfg *config.ProjectConfig, opts initFactoryOptions) error {
	if !initFactoryStep(ctx, workDir, streams, cfg, opts) {
		return nil
	}
	return writeProjectConfig(workDir, cfg, streams)
}
