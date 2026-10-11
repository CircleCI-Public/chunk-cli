package sandboximage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/brand"
	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/sidecar"
)

// Verification steps, in the order they run.
const (
	StepKit     = "agent kit"
	StepInstall = "install"
	StepGate    = "gate"
)

// kitCheck proves the tools the factory's own scripts and agent need exist.
const kitCheck = "git --version && claude --version && sha256sum --version && pkill -V"

// outputTailBytes bounds the output kept from a failed step.
const outputTailBytes = 4000

// verifyBackend is the part of a sidecar backend Verify drives. Using the
// docker provider keeps the transport the same as a real factory run.
type verifyBackend interface {
	Create(ctx context.Context, name, image, repoPath string) (sidecar.Instance, error)
	Sync(ctx context.Context, id, repoPath, cwd string, retry bool, status iostream.StatusFunc) error
	Exec(ctx context.Context, id, script string, env map[string]string, onOutput iostream.OutputFn, onSubmitted func(string)) (int, error)
	Delete(ctx context.Context, id string) error
}

// VerifyOptions say what to verify.
type VerifyOptions struct {
	Dir   string // the repo
	Image string // the tag to verify
	// Install is the dependency install command, empty for none.
	Install string
	// Gate is the cheapest validation command, nil for none.
	Gate     *config.Command
	Progress iostream.StatusFunc
}

// VerifyResult is the outcome of Verify. Step is empty when everything passed;
// otherwise it names the first step that failed.
type VerifyResult struct {
	Step       string
	ExitCode   int
	OutputTail string
}

// OK reports whether every step passed.
func (r VerifyResult) OK() bool { return r.Step == "" }

// Describe is a one-line account of a failure, for prompts and messages.
func (r VerifyResult) Describe() string {
	return fmt.Sprintf("verification failed at %s (exit %d):\n%s", r.Step, r.ExitCode, r.OutputTail)
}

// PickGate returns the cheapest configured validation command, or nil.
func PickGate(commands []config.Command) *config.Command {
	// Cheapest first.
	for _, name := range [...]string{"lint", "format", "typecheck", "test"} {
		for i := range commands {
			if commands[i].Name == name && commands[i].Run != "" {
				return &commands[i]
			}
		}
	}
	return nil
}

// Verify runs the image in a throwaway container over a scratch copy of the
// repo, mounted where a factory run mounts it (named after the origin remote): the agent kit, then the install
// step, then one gate, stopping at the first failure. The container and the
// scratch copy are always removed.
func Verify(ctx context.Context, b verifyBackend, opts VerifyOptions) (VerifyResult, error) {
	status := opts.Progress
	if status == nil {
		status = func(iostream.Level, string) {}
	}
	repoPath, err := sidecar.RepoWorkspace(ctx, opts.Dir)
	if err != nil {
		return VerifyResult{}, err
	}
	inst, err := b.Create(ctx, brand.Command+"-verify", opts.Image, repoPath)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("start verification container: %w", err)
	}
	defer func() {
		// The caller's context may be what ended the run.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = b.Delete(cleanup, inst.ID)
	}()
	if err := b.Sync(ctx, inst.ID, repoPath, opts.Dir, false, func(iostream.Level, string) {}); err != nil {
		return VerifyResult{}, fmt.Errorf("copy the repo into the container: %w", err)
	}

	steps := []verifyStep{{StepKit, kitCheck, time.Minute}}
	if opts.Install != "" {
		steps = append(steps, verifyStep{StepInstall, opts.Install, config.DefaultInstallTimeout * time.Second})
	}
	if opts.Gate != nil {
		steps = append(steps, verifyStep{StepGate, opts.Gate.Run, time.Duration(opts.Gate.EffectiveTimeout()) * time.Second})
	}
	for _, step := range steps {
		res, err := runStep(ctx, b, inst, repoPath, step.name, step.script, step.timeout)
		if err != nil || !res.OK() {
			return res, err
		}
		status(iostream.LevelDone, step.name+" ✓")
	}
	return VerifyResult{}, nil
}

// verifyStep is one script Verify runs.
type verifyStep struct {
	name, script string
	timeout      time.Duration
}

// runStep runs one script in the repo and reports a non-zero exit as a failed
// VerifyResult rather than an error.
func runStep(ctx context.Context, b verifyBackend, inst sidecar.Instance, repoPath, name, script string, timeout time.Duration) (VerifyResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out tail
	var partial strings.Builder
	code, err := b.Exec(ctx, inst.ID, "cd "+sidecar.ShellEscape(repoPath)+" && "+script, nil, func(_ string, data []byte) {
		partial.Write(data)
		for {
			s := partial.String()
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				break
			}
			out.add(s[:i])
			partial.Reset()
			partial.WriteString(s[i+1:])
		}
	}, nil)
	if partial.Len() > 0 {
		out.add(partial.String())
	}
	if err != nil {
		if ctx.Err() != nil {
			return VerifyResult{Step: name, ExitCode: -1, OutputTail: lastBytes(out.String()) + "\n(timed out)"}, nil
		}
		return VerifyResult{}, fmt.Errorf("run %s: %w", name, err)
	}
	if code != 0 {
		return VerifyResult{Step: name, ExitCode: code, OutputTail: lastBytes(out.String())}, nil
	}
	return VerifyResult{}, nil
}

func lastBytes(s string) string {
	if len(s) > outputTailBytes {
		return s[len(s)-outputTailBytes:]
	}
	return s
}
