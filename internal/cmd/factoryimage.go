package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/smeltdev/smelt/internal/codexauth"
	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/all"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/sandboximage"
	"github.com/smeltdev/smelt/internal/sidecar/docker"
	"github.com/smeltdev/smelt/internal/ui"
)

// suggestionFactoryImage is what to run after a failed or stale image build.
const suggestionFactoryImage = "Fix Dockerfile.smelt if needed, then run: smelt factory image"

// imageBuildLog is where the full log of a sandbox image build is kept.
func imageBuildLog(root string) string {
	dir, err := config.ProjectDataDir(root)
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "image-build.log")
}

// buildSandboxImage builds the image for dockerfile unless its tag is already on
// the Docker host, and returns the tag. reason is shown when it builds.
func buildSandboxImage(ctx context.Context, streams iostream.Streams, root, dockerfile, reason string, rebuild bool) (string, error) {
	plan, err := sandboximage.NewPlan(root, dockerfile)
	if err != nil {
		return "", newUserError(fmt.Sprintf("Could not read %s.", dockerfile)).
			withCode("factory.image_dockerfile").
			withSuggestion("Run smelt factory image to write it, or smelt init.").
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	// Wait out a starting engine first: it would answer the build's image
	// lookup with "no such image" and trigger a needless rebuild.
	provider, err := docker.New()
	if err != nil {
		return "", dockerUnreachable(err)
	}
	defer func() { _ = provider.Close() }()
	if err := provider.WaitEngine(ctx, func(_ iostream.Level, msg string) { streams.ErrPrintln(ui.Dim("  ▸ " + msg)) }); err != nil {
		return "", dockerUnreachable(err)
	}
	builder, err := sandboximage.Connect()
	if err != nil {
		return "", dockerUnreachable(err)
	}
	defer func() { _ = builder.Close() }()

	_, err = builder.Ensure(ctx, plan, sandboximage.EnsureOptions{
		Rebuild: rebuild,
		LogPath: imageBuildLog(root),
		Building: func(tag string) {
			streams.ErrPrintf("Building factory image %s (%s)\n", tag, reason)
		},
		Progress: func(_ iostream.Level, msg string) { streams.ErrPrintln(ui.Dim("  ▸ " + msg)) },
	})
	var buildErr *sandboximage.BuildError
	switch {
	case errors.As(err, &buildErr):
		return "", newUserError(fmt.Sprintf("Building %s failed.", buildErr.Tag)).
			withCode("factory.image_build_failed").
			withDetail(buildErr.LogTail).
			withSuggestion(suggestionFactoryImage + logPathHint(buildErr.LogPath)).
			wrap(err)
	case err != nil:
		return "", &userError{msg: "Could not build the factory image.", suggestion: suggestionFactoryImage, err: err}
	}
	return plan.Tag, nil
}

func logPathHint(path string) string {
	if path == "" {
		return ""
	}
	return " (full log: " + path + ")"
}

// resolveBuildSettings is resolveFactorySettings plus, for a factory.dockerfile
// image, the build of that image when it is missing. The daemon only ever sees
// the resulting tag.
func resolveBuildSettings(ctx context.Context, streams iostream.Streams, root string, flags factorySettings, set factoryExplicit, cfg *config.ProjectConfig) (factorySettings, error) {
	s, err := resolveFactorySettings(flags, set, cfg)
	if err != nil || s.ImageSource != imageFromDockerfile {
		return s, err
	}
	s.KitWarning = noteKitOutdated(streams, kitNotice{Root: root, Dockerfile: s.Dockerfile, Now: time.Now()})
	s.Image, err = buildSandboxImage(ctx, streams, root, s.Dockerfile, s.Dockerfile+" changed", false)
	return s, err
}

func newFactoryImageCmd() *cobra.Command {
	var rebuild, regenerate, updateKitFlag, yes bool
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Build the Docker image factory runs execute in",
		Long: `Build the image described by factory.dockerfile (Dockerfile.smelt, written by
smelt init) if it is not on this Docker host yet. smelt factory build does the
same before a run; use this command after editing Dockerfile.smelt by hand or
when the build failed.

The image's tag is derived from the Dockerfile and the dependency files it
COPYs, so changing either makes the next build a new image. The Docker layer
cache keeps unchanged layers cheap.

Dockerfile.smelt is never overwritten without asking. If it is missing, it is
generated from detection and the CI config.

--rebuild builds even when the tag exists, without the layer cache.
--regenerate renders Dockerfile.smelt again, shows the diff and asks before
writing it. It re-runs detection, so hand edits are lost.
--update-kit replaces only the agent-kit section of Dockerfile.smelt (from its
"# --- smelt agent kit" line to the end) with the current one, keeps everything
above it, shows the diff and asks before writing; --yes skips the question.
The two are mutually exclusive.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if regenerate && updateKitFlag {
				return newUserError("--regenerate and --update-kit cannot be used together.").
					withCode("command.invalid_args").
					withSuggestion("Use --update-kit to replace only the agent kit, or --regenerate to render the whole Dockerfile again.").
					withExitCode(ExitBadArgs).
					withoutDetail()
			}
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)
			root, err := sessionProjectRoot(ctx)
			if err != nil {
				return err
			}
			cfg, err := config.LoadProjectConfig(root)
			if err != nil {
				return &userError{msg: msgValidateNotConfigured, suggestion: suggestionRunInit, err: err, blocked: true}
			}
			dockerfile := sandboximage.DefaultDockerfile
			if cfg.Factory != nil && cfg.Factory.Dockerfile != "" {
				dockerfile = cfg.Factory.Dockerfile
			}
			if updateKitFlag {
				err = updateKit(ctx, streams, kitUpdate{Root: root, Dockerfile: dockerfile, Yes: yes})
			} else {
				err = prepareDockerfile(ctx, streams, root, dockerfile, cfg, regenerate)
			}
			if err != nil {
				return err
			}
			noteKitOutdated(streams, kitNotice{Root: root, Dockerfile: dockerfile, Now: time.Now(), EveryTime: true})
			tag, err := readyImage(ctx, streams, root, dockerfile, cfg, rebuild)
			if err != nil {
				return err
			}
			streams.ErrPrintln(ui.Success("Factory image ready: " + tag))
			return nil
		},
	}
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "build even when the image exists, without the layer cache")
	cmd.Flags().BoolVar(&regenerate, "regenerate", false, "render the Dockerfile again from detection, show the diff and confirm before writing")
	cmd.Flags().BoolVar(&updateKitFlag, "update-kit", false, "replace only the agent-kit section of the Dockerfile with the current one, show the diff and confirm before writing")
	cmd.Flags().BoolVar(&yes, "yes", false, "with --update-kit, write without asking")
	return cmd
}

// prepareDockerfile makes sure dockerfile exists: it writes a generated one if
// missing, and with regenerate offers to replace the existing one.
func prepareDockerfile(ctx context.Context, streams iostream.Streams, root, dockerfile string, cfg *config.ProjectConfig, regenerate bool) error {
	_, statErr := os.Stat(filepath.Join(root, dockerfile))
	exists := statErr == nil
	if exists && !regenerate {
		return nil
	}
	gen, err := generateDockerfile(ctx, root, cfg)
	if err != nil {
		return err
	}
	content := gen.Dockerfile
	if !exists {
		if err := sandboximage.WriteDockerfile(root, dockerfile, content); err != nil {
			return &userError{msg: "Could not write " + dockerfile + ".", err: err}
		}
		streams.ErrPrintln(ui.Success("Wrote " + dockerfile))
		return nil
	}
	diff, err := sandboximage.Diff(ctx, root, dockerfile, content)
	if err != nil {
		return &userError{msg: "Could not compare " + dockerfile + ".", err: err}
	}
	if diff == "" {
		streams.ErrPrintln(dockerfile + " already matches what smelt would generate.")
		return nil
	}
	streams.ErrPrintln(diff)
	if !isInteractive() {
		return newUserError(dockerfile + " differs from the regenerated one.").
			withCode("command.invalid_args").
			withSuggestion("Run smelt factory image --regenerate in a terminal to confirm the overwrite.").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	ok, err := ui.Confirm("Overwrite "+dockerfile+"?", false)
	if err != nil || !ok {
		return newUserError(dockerfile + " left as it was.").withCode("command.cancelled").withExitCode(ExitGeneral).withoutDetail()
	}
	if err := os.WriteFile(filepath.Join(root, dockerfile), []byte(content), 0o644); err != nil {
		return &userError{msg: "Could not write " + dockerfile + ".", err: err}
	}
	streams.ErrPrintln(ui.Success("Wrote " + dockerfile))
	return nil
}

// generateDockerfile renders a Dockerfile for root from detection and the CI
// config, preferring the environment already saved in cfg.
func generateDockerfile(ctx context.Context, root string, cfg *config.ProjectConfig) (sandboximage.Generated, error) {
	builder, err := sandboximage.Connect()
	if err != nil {
		return sandboximage.Generated{}, dockerUnreachable(err)
	}
	defer func() { _ = builder.Close() }()
	gen, err := builder.Generate(ctx, root, cfg.Environment)
	if err != nil {
		return gen, &userError{msg: "Could not generate a Dockerfile for this repo.", err: err}
	}
	return gen, nil
}

// imageAttempt is one build-and-verify of the image: Failure is empty when it
// is ready, and otherwise says what went wrong in terms Claude can act on.
type imageAttempt struct {
	Tag     string
	Failure string
}

// attemptImage builds the image and verifies it. A failed build or verification
// is reported in the result; only a problem with Docker itself is an error.
func attemptImage(ctx context.Context, streams iostream.Streams, root, dockerfile string, cfg *config.ProjectConfig, rebuild bool) (imageAttempt, error) {
	tag, err := buildSandboxImage(ctx, streams, root, dockerfile, dockerfile+" changed", rebuild)
	var buildErr *sandboximage.BuildError
	if errors.As(err, &buildErr) {
		return imageAttempt{Failure: "docker build failed:\n" + buildErr.LogTail}, nil
	}
	if err != nil {
		return imageAttempt{}, err
	}
	provider, err := docker.New()
	if err != nil {
		return imageAttempt{}, dockerUnreachable(err)
	}
	defer func() { _ = provider.Close() }()
	provider.ManagedDockerfile = dockerfile
	status := func(_ iostream.Level, msg string) { streams.ErrPrintln(ui.Dim("  ▸ " + msg)) }
	if err := provider.Ready(ctx, tag, status); err != nil {
		return imageAttempt{}, dockerUnreachable(err)
	}
	res, err := sandboximage.Verify(ctx, provider, sandboximage.VerifyOptions{
		Dir: root, Image: tag, Install: cfg.InstallCommand(), Gate: sandboximage.PickGate(cfg.Commands),
		Progress: status, Codex: codexSmoke(),
	})
	if err != nil {
		return imageAttempt{}, &userError{msg: "Could not verify the factory image.", suggestion: suggestionFactoryImage, err: err}
	}
	if !res.OK() {
		return imageAttempt{Tag: tag, Failure: res.Describe()}, nil
	}
	return imageAttempt{Tag: tag}, nil
}

// codexSmoke is the Codex turn the kit check runs when this machine already has
// a Codex login, and nil when it has none. It never prompts or logs in: an
// expired login only makes the turn unconfirmed.
func codexSmoke() *sandboximage.CodexSmoke {
	login, err := codexauth.Find()
	if err != nil {
		return nil
	}
	cred, err := login.Credential()
	if err != nil {
		return nil
	}
	h, err := all.ByName(harness.Codex)
	if err != nil {
		return nil
	}
	return &sandboximage.CodexSmoke{Harness: h, Credential: cred}
}

// readyImage builds and verifies the image. When that fails in a terminal with
// claude installed, it offers to let Claude repair Dockerfile.smelt, up to
// sandboximage.MaxRepairAttempts times.
func readyImage(ctx context.Context, streams iostream.Streams, root, dockerfile string, cfg *config.ProjectConfig, rebuild bool) (string, error) {
	first, err := attemptImage(ctx, streams, root, dockerfile, cfg, rebuild)
	if err != nil || first.Failure == "" {
		return first.Tag, err
	}
	failed := newUserError("The factory image failed its checks.").
		withCode("factory.image_verify_failed").
		withDetail(first.Failure).
		withSuggestion(suggestionFactoryImage + logPathHint(imageBuildLog(root)))
	state := sandboximage.RepairState{Interactive: isInteractive(), HaveClaude: sandboximage.HaveClaude()}
	if sandboximage.DecideRepair(state) == sandboximage.RepairAsk {
		ok, err := ui.Confirm(fmt.Sprintf("Let Claude try to fix %s? (up to %d attempts)", dockerfile, sandboximage.MaxRepairAttempts), false)
		warnPromptError(streams, err)
		state.Consent = &ok
	}
	if sandboximage.DecideRepair(state) != sandboximage.RepairAttempt {
		return "", failed
	}
	return repairImage(ctx, streams, root, dockerfile, cfg, first.Failure, failed)
}

// repairImage runs the repair loop and reports the outcome. failed is the
// error to return when the repair does not work.
func repairImage(ctx context.Context, streams iostream.Streams, root, dockerfile string, cfg *config.ProjectConfig, failure string, failed error) (string, error) {
	original, err := os.ReadFile(filepath.Join(root, dockerfile))
	if err != nil {
		return "", &userError{msg: "Could not read " + dockerfile + ".", err: err}
	}
	var token string
	if rc, err := config.Resolve("", "", false); err == nil {
		token = rc.ClaudeOAuthToken
	}
	var final imageAttempt
	res, err := sandboximage.RunRepair(ctx, sandboximage.RepairOptions{
		Dir: root, Dockerfile: dockerfile, Failure: failure,
		Fixer:     sandboximage.ClaudeFixer{Token: token},
		OnAttempt: func(n int) { streams.ErrPrintf("Repair attempt %d of %d...\n", n, sandboximage.MaxRepairAttempts) },
		Retry: func(ctx context.Context) (string, error) {
			final, err = attemptImage(ctx, streams, root, dockerfile, cfg, false)
			return final.Failure, err
		},
	})
	switch {
	case errors.Is(err, sandboximage.ErrOtherFilesChanged):
		return "", newUserError("Claude changed files other than " + dockerfile + ".").
			withCode("factory.image_repair_scope").
			withSuggestion("Review the changes with git status and revert what you don't want.").
			withoutDetail()
	case err != nil:
		return "", &userError{msg: "The repair could not finish.", suggestion: suggestionFactoryImage, err: err}
	case !res.Fixed:
		streams.ErrPrintln(ui.Warning(fmt.Sprintf("%s is left as the last attempt edited it.", dockerfile)))
		return "", failed
	}
	if diff, err := sandboximage.DiffText(ctx, dockerfile, string(original), readFileOrEmpty(filepath.Join(root, dockerfile))); err == nil {
		streams.ErrPrintln(diff)
	}
	streams.ErrPrintln(ui.Success("Fixed in " + fmt.Sprint(res.Attempts) + " attempt(s). Commit " + dockerfile + " so the team shares it."))
	return final.Tag, nil
}

func readFileOrEmpty(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// dockerUnreachable is the error for a Docker daemon smelt could not connect to.
func dockerUnreachable(err error) error {
	return &userError{msg: "Could not reach Docker.", suggestion: "Start Docker, then try again.", err: err}
}
