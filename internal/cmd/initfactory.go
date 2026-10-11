package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/envbuilder"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sandboximage"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

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
	// SkipImageBuild is --skip-image-build: write Dockerfile.chunk but do not
	// offer to build it.
	SkipImageBuild bool
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
	switch {
	case f.Image != "":
		return fmt.Sprintf("Factory runs: %s (%s)", backend, f.Image)
	case f.Dockerfile != "":
		return fmt.Sprintf("Factory runs: %s (built from %s)", backend, f.Dockerfile)
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
		initManagedImage(ctx, workDir, streams, cfg, opts)
		return true
	case factoryAsk:
		// Handled below: asking is the only action that needs the prompts.
	}

	backend, ok := askFactory(streams)
	if !ok {
		return false
	}
	applyFactory(cfg, streams, backend, "")
	initManagedImage(ctx, workDir, streams, cfg, opts)
	if opts.Author == "" {
		if author, ok := askAuthor(streams, factory.ResolveAuthor(ctx, workDir, config.CommitAuthor{})); ok && author != "" {
			cfg.Factory.Author = author
			streams.ErrPrintln(ui.Dim("Saved factory.author; .chunk/config.json is committed, so everyone here commits as this author."))
		}
	}
	return true
}

// askFactory asks where factory runs execute. ok is false when the user
// cancelled or the prompt could not be shown.
func askFactory(streams iostream.Streams) (backend string, ok bool) {
	items := []string{"docker (local containers)", "circleci (cloud sidecars)"}
	idx, err := ui.SelectFromList("Where should factory runs execute?", items)
	if err != nil {
		warnPromptError(streams, err)
		return "", false
	}
	if idx != 0 {
		return config.BackendCircleCI, true
	}
	return config.BackendDocker, true
}

// warnPromptError reports a prompt that failed for a reason other than there
// being no terminal or the user cancelling, which need no explanation.
func warnPromptError(streams iostream.Streams, err error) {
	if err == nil || errors.Is(err, ui.ErrNoTTY) || errors.Is(err, ui.ErrCancelled) {
		return
	}
	streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not ask about factory runs: %v", err)))
}

// applyFactory records the backend and, when one was given, the image, keeping any other
// factory settings already in the config, and says what it recorded.
func applyFactory(cfg *config.ProjectConfig, streams iostream.Streams, backend, image string) {
	if cfg.Factory == nil {
		cfg.Factory = &config.FactoryConfig{}
	}
	cfg.Factory.Backend = backend
	if image != "" {
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

// managesImage reports whether init should write and build a Dockerfile for the
// docker backend: an explicit factory.image, from a flag or already saved,
// stays the user's to manage.
func managesImage(cfg *config.ProjectConfig) bool {
	return cfg.Factory != nil && config.BackendOrDefault(cfg.Factory.Backend) == config.BackendDocker && cfg.Factory.Image == ""
}

// initManagedImage is the docker half of the factory step: it writes
// Dockerfile.chunk unless one exists, records it as factory.dockerfile, and
// offers to build the image.
func initManagedImage(ctx context.Context, workDir string, streams iostream.Streams, cfg *config.ProjectConfig, opts initFactoryOptions) {
	if !managesImage(cfg) {
		return
	}
	dockerfile := sandboximage.DefaultDockerfile
	saveDetectedEnvironment(ctx, workDir, streams, cfg)
	if err := writeInitDockerfile(ctx, workDir, streams, cfg, dockerfile); err != nil {
		streams.ErrPrintf("%s\n", ui.Warning(err.Error()))
		streams.ErrPrintln(ui.Dim("Once Docker is running: chunk factory image"))
		return
	}
	cfg.Factory.Dockerfile = dockerfile
	streams.ErrPrintln(ui.Dim(dockerfile + " is yours to edit and commit; chunk rebuilds the image when it or a dependency lockfile changes."))

	if opts.SkipImageBuild || !isInteractive() {
		streams.ErrPrintln(ui.Dim("Build the factory image with: chunk factory image"))
		return
	}
	build, err := ui.Confirm("Build the factory image now? (a few minutes the first time)", true)
	if err != nil || !build {
		warnPromptError(streams, err)
		streams.ErrPrintln(ui.Dim("Build it later with: chunk factory image (chunk factory build also does it)"))
		return
	}
	tag, err := readyImage(ctx, streams, workDir, dockerfile, cfg, false)
	if err != nil {
		streams.ErrPrintf("%s\n", ui.Warning(errorSummary(err)))
		return
	}
	streams.ErrPrintln(ui.Success("Ready: " + tag + ". Try: chunk factory build --plan spec.md"))
}

// writeInitDockerfile writes dockerfile from detection, or keeps and says so
// when it is already there.
func writeInitDockerfile(ctx context.Context, workDir string, streams iostream.Streams, cfg *config.ProjectConfig, dockerfile string) error {
	if _, err := os.Stat(filepath.Join(workDir, dockerfile)); err == nil {
		streams.ErrPrintln("Keeping existing " + dockerfile)
		return nil
	}
	gen, err := generateInitDockerfile(ctx, workDir, cfg)
	if err != nil {
		return err
	}
	r := gen.Recipe
	streams.ErrPrintf("Detected: %s · %s (%s)\n", r.Env.Stack, r.Base, r.BaseFrom)
	if r.Unknown() {
		streams.ErrPrintln(ui.Warning("Stack not recognised: add your toolchain to " + dockerfile + "."))
	}
	if err := sandboximage.WriteDockerfile(workDir, dockerfile, gen.Dockerfile); err != nil {
		return err
	}
	streams.ErrPrintln(ui.Success("Wrote " + dockerfile))
	return nil
}

// errorSummary is an error's user-facing message plus its detail, if any.
func errorSummary(err error) string {
	var ue *userError
	if !errors.As(err, &ue) {
		return err.Error()
	}
	msg := ue.UserMessage()
	if d := ue.Detail(); d != "" {
		msg += "\n" + d
	}
	if sug := ue.Suggestion(); sug != "" {
		msg += "\n" + sug
	}
	return msg
}

// generateInitDockerfile renders the Dockerfile for init. It is a variable so
// tests can render without a Docker daemon, which is only needed to read the
// base image's user.
var generateInitDockerfile = generateDockerfile

// saveDetectedEnvironment records the detected environment in cfg unless one is
// already saved. The factory's per-sidecar install and the image check read
// the install step from it; the image holds only the package manager's store.
func saveDetectedEnvironment(ctx context.Context, workDir string, streams iostream.Streams, cfg *config.ProjectConfig) {
	if cfg.Environment != nil {
		return
	}
	env, err := envbuilder.DetectEnvironment(ctx, workDir)
	if err != nil {
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not detect the environment: %v", err)))
		return
	}
	cfg.Environment = env.ForConfig()
}
