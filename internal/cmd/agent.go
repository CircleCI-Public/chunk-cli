package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/term"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/closer"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/daemon"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/secrets"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/tui/agentsession"
)

const agentDaemonPort = "7777"

func newAgentCmd() *cobra.Command {
	var identityFile, anthropicKeyRef, orgID, image string
	var doSync bool

	cmd := &cobra.Command{
		Use:          "agent",
		Short:        "Run Claude Code in a sidecar with a live validation dashboard",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAgentCmd(cmd, identityFile, anthropicKeyRef, orgID, image, doSync)
		},
	}
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file (uses ssh-agent or ~/.ssh/chunk_ai when omitted)")
	cmd.Flags().StringVar(&anthropicKeyRef, "anthropic-key-ref", "", "1Password secret reference for the Anthropic API key (e.g. op://vault/item/field)")
	cmd.Flags().StringVar(&orgID, "org-id", "", "CircleCI org ID (required when creating a new sidecar and no org is configured)")
	cmd.Flags().StringVar(&image, "image", "6c71e3eb-feec-480d-8422-f95859bd8d6f", "Snapshot ID to use when auto-creating a sidecar")
	cmd.Flags().BoolVar(&doSync, "sync", false, "Sync the local working directory to the sidecar before starting")
	return cmd
}

func runAgentCmd(cmd *cobra.Command, identityFile, anthropicKeyRef, orgID, image string, doSync bool) error {
	if err := tui.RequireStdoutTTY(); err != nil {
		return fmt.Errorf("agent requires a TTY")
	}

	ctx := cmd.Context()
	streams := iostream.FromCmd(cmd)
	insecureStorage := insecureStorageFlag(cmd)

	rc, _ := config.ResolveCircleCI(insecureStorage)
	circleCIClient, err := ensureCircleCIClient(ctx, cmd, rc, streams, tui.PromptHidden)
	if err != nil {
		return fmt.Errorf("circleci auth: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	authSock := os.Getenv(config.EnvSSHAuthSock)

	w, h, sizeErr := term.GetSize(int(os.Stdout.Fd()))
	if sizeErr != nil {
		w, h = 220, 50
	}

	dataDir, _ := sidecar.StateDir()

	setupCh := make(chan agentsession.SetupEvent, 16)
	go runAgentSetup(ctx, circleCIClient, orgID, image, identityFile, authSock, anthropicKeyRef, cwd, agentDaemonPort, doSync, setupCh)

	stepLabels := agentSetupStepLabels(doSync)

	factory := func(sidecarID, sidecarName string, ch chan<- agentsession.SetupEvent) {
		runAgentSetupForID(ctx, circleCIClient, identityFile, authSock, anthropicKeyRef, agentDaemonPort, sidecarID, sidecarName, 1, ch)
	}

	m := agentsession.NewWithSetup(stepLabels, setupCh, cwd, dataDir, factory, w, h)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	_, err = p.Run()
	return err
}

// agentSetupStepLabels returns the setup step labels for chunk agent.
// When doSync is true a "Syncing workspace" step is inserted after "Locating sidecar".
func agentSetupStepLabels(doSync bool) []string {
	labels := []string{"Locating sidecar"}
	if doSync {
		labels = append(labels, "Syncing workspace")
	}
	return append(labels,
		"Opening SSH session",
		"Connecting via SSH",
		"Copying chunk binary",
		"Installing chunk binary",
		"Starting daemon",
	)
}

func runAgentSetup(
	ctx context.Context,
	client *circleci.Client,
	orgID, image, identityFile, authSock, anthropicKeyRef, cwd, port string,
	doSync bool,
	ch chan<- agentsession.SetupEvent,
) {
	emit := func(e agentsession.SetupEvent) { ch <- e }

	// Step 0: locate/create sidecar
	emit(agentsession.SetupEvent{StepIndex: 0, Running: true})
	sidecarID, sidecarName, err := ensureAgentSidecar(ctx, client, orgID, image, cwd, func(string, ...any) {})
	if err != nil {
		emit(agentsession.SetupEvent{StepIndex: 0, Err: err})
		return
	}
	emit(agentsession.SetupEvent{StepIndex: 0})

	baseStep := 1
	if doSync {
		// Step 1: sync workspace
		emit(agentsession.SetupEvent{StepIndex: 1, Running: true})
		if err := sidecarSetupSync(ctx, client, sidecarID, identityFile, authSock, true, cwd, func(iostream.Level, string) {}); err != nil {
			emit(agentsession.SetupEvent{StepIndex: 1, Err: err})
			return
		}
		emit(agentsession.SetupEvent{StepIndex: 1})
		baseStep = 2
	}

	runAgentSetupForID(ctx, client, identityFile, authSock, anthropicKeyRef, port, sidecarID, sidecarName, baseStep, ch)
}

// runAgentSetupForID runs setup steps for a known sidecar ID/name starting at baseStep.
// baseStep is 1 when there is no sync step, 2 when sync precedes it.
func runAgentSetupForID(
	ctx context.Context,
	client *circleci.Client,
	identityFile, authSock, anthropicKeyRef, port string,
	sidecarID, sidecarName string,
	baseStep int,
	ch chan<- agentsession.SetupEvent,
) {
	emit := func(e agentsession.SetupEvent) { ch <- e }

	s := baseStep

	// s+0: open SSH session
	emit(agentsession.SetupEvent{StepIndex: s, Running: true})
	session, err := sidecar.OpenSession(ctx, client, sidecarID, identityFile, authSock)
	if err != nil {
		emit(agentsession.SetupEvent{StepIndex: s, Err: fmt.Errorf("open SSH session: %w", err)})
		return
	}
	emit(agentsession.SetupEvent{StepIndex: s})

	// s+1: dial SSH
	emit(agentsession.SetupEvent{StepIndex: s + 1, Running: true})
	sshConn, err := sidecar.DialSSH(ctx, session)
	if err != nil {
		emit(agentsession.SetupEvent{StepIndex: s + 1, Err: fmt.Errorf("dial SSH: %w", err)})
		return
	}
	emit(agentsession.SetupEvent{StepIndex: s + 1})

	// s+2: copy chunk binary
	emit(agentsession.SetupEvent{StepIndex: s + 2, Running: true})
	if err := copyChunkBinary(sshConn); err != nil {
		_ = sshConn.Close()
		emit(agentsession.SetupEvent{StepIndex: s + 2, Err: fmt.Errorf("copy binary: %w", err)})
		return
	}
	emit(agentsession.SetupEvent{StepIndex: s + 2})

	// s+3: install chunk binary to PATH
	emit(agentsession.SetupEvent{StepIndex: s + 3, Running: true})
	if err := installChunkBinary(sshConn); err != nil {
		_ = sshConn.Close()
		emit(agentsession.SetupEvent{StepIndex: s + 3, Err: fmt.Errorf("install binary: %w", err)})
		return
	}
	emit(agentsession.SetupEvent{StepIndex: s + 3})

	// s+4: start daemon
	emit(agentsession.SetupEvent{StepIndex: s + 4, Running: true})
	if err := startRemoteDaemon(sshConn, port); err != nil {
		_ = sshConn.Close()
		emit(agentsession.SetupEvent{StepIndex: s + 4, Err: fmt.Errorf("start daemon: %w", err)})
		return
	}
	emit(agentsession.SetupEvent{StepIndex: s + 4})

	dc := daemon.NewClientOverSSH(sshConn, "localhost:"+port)
	_ = dc.UpsertSidecar(ctx, sidecarID, sidecarName, daemon.SyncStateNotSynced, "")

	envVars, err := resolveAgentEnvVars(ctx, anthropicKeyRef)
	if err != nil {
		emit(agentsession.SetupEvent{StepIndex: s + 4, Err: fmt.Errorf("resolve secrets: %w", err)})
		_ = sshConn.Close()
		return
	}

	emit(agentsession.SetupEvent{
		Result: &agentsession.SetupResult{
			Session:      session,
			SidecarID:    sidecarID,
			SidecarName:  sidecarName,
			DaemonClient: dc,
			SSHConn:      sshConn,
			EnvVars:      envVars,
		},
	})
}

// ensureAgentSidecar returns the active sidecar for the current project, creating
// one with the "claude" image if none exists.
func ensureAgentSidecar(
	ctx context.Context,
	client *circleci.Client,
	orgID, image, workDir string,
	printf func(string, ...any),
) (id, name string, err error) {
	active, err := sidecar.LoadActive(ctx)
	if err != nil {
		return "", "", &userError{msg: msgCouldNotLoadSidecar, suggestion: configFilePermHint, err: err}
	}
	if active != nil {
		return active.SidecarID, active.Name, nil
	}

	resolvedOrgID, err := resolveOrgID(orgID, workDir, orgPicker(ctx, client))
	if err != nil {
		return "", "", err
	}

	scName := randomSidecarName()
	printf("Creating sidecar %q...\n", scName)
	sc, err := sidecar.Create(ctx, client, resolvedOrgID, scName, image)
	if err != nil {
		if authErr := notAuthorized("create sidecars", err); authErr != nil {
			return "", "", authErr
		}
		return "", "", &userError{msg: msgCouldNotCreateSidecar, suggestion: suggestionNetworkRetry, err: err}
	}

	if saveErr := sidecar.SaveActive(ctx, sidecar.ActiveSidecar{SidecarID: sc.ID, Name: sc.Name, OrgID: resolvedOrgID}); saveErr != nil {
		printf("warning: could not save active sidecar: %v\n", saveErr)
	}
	printf("Created sidecar %s (%s)\n", sc.Name, sc.ID)
	return sc.ID, sc.Name, nil
}

// resolveAgentEnvVars builds the env var map to inject into the sidecar session.
// ANTHROPIC_API_KEY is sourced from --anthropic-key-ref (a 1Password op:// reference),
// falling back to the value already set in the local environment.
func resolveAgentEnvVars(ctx context.Context, anthropicKeyRef string) (map[string]string, error) {
	var rawValue string
	switch {
	case anthropicKeyRef != "":
		rawValue = anthropicKeyRef
	case os.Getenv("ANTHROPIC_API_KEY") != "":
		rawValue = os.Getenv("ANTHROPIC_API_KEY")
	}
	if rawValue == "" {
		return nil, nil
	}
	env := map[string]string{"ANTHROPIC_API_KEY": rawValue}
	resolved, err := secrets.ResolveAll(ctx, env, nil)
	if err != nil {
		return nil, fmt.Errorf("resolve Anthropic key: %w", err)
	}
	return resolved, nil
}

// linuxBinaryPath returns the path to a linux/amd64 chunk binary and a cleanup
// func. If already running on Linux it returns the current executable with a
// no-op cleanup; otherwise it cross-compiles on the fly into a temp file.
func linuxBinaryPath() (path string, cleanup func(), err error) {
	noop := func() {}
	if runtime.GOOS == "linux" {
		exe, err := os.Executable()
		if err != nil {
			return "", noop, fmt.Errorf("find executable: %w", err)
		}
		return exe, noop, nil
	}

	tmp, err := os.CreateTemp("", "chunk-linux-amd64-*")
	if err != nil {
		return "", noop, fmt.Errorf("temp file: %w", err)
	}
	_ = tmp.Close()
	cleanup = func() { _ = os.Remove(tmp.Name()) }

	cwd, err := os.Getwd()
	if err != nil {
		cleanup()
		return "", noop, fmt.Errorf("get cwd: %w", err)
	}

	//nolint:gosec // intentional cross-compile invocation
	cmd := exec.Command("go", "build", "-o", tmp.Name(), ".")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("cross-compile: %s: %w", out, err)
	}
	return tmp.Name(), cleanup, nil
}

// copyChunkBinary streams a linux/amd64 chunk binary to /tmp/chunk on the
// sidecar. When running on a non-Linux host it cross-compiles on the fly.
func copyChunkBinary(conn *sidecar.SSHConn) (err error) {
	binPath, cleanupBin, buildErr := linuxBinaryPath()
	if buildErr != nil {
		return fmt.Errorf("prepare linux binary: %w", buildErr)
	}
	defer cleanupBin()

	f, openErr := os.Open(binPath)
	if openErr != nil {
		return fmt.Errorf("open executable: %w", openErr)
	}
	defer closer.ErrorHandler(f, &err)

	sess, sessErr := conn.NewSession()
	if sessErr != nil {
		return fmt.Errorf("ssh session: %w", sessErr)
	}
	defer func() { _ = sess.Close() }()

	sess.Stdin = f
	sess.Stdout = io.Discard
	sess.Stderr = io.Discard
	if runErr := sess.Run("tee /tmp/chunk"); runErr != nil {
		return fmt.Errorf("upload binary: %w", runErr)
	}

	chmodSess, chmodErr := conn.NewSession()
	if chmodErr != nil {
		return fmt.Errorf("ssh session: %w", chmodErr)
	}
	defer func() { _ = chmodSess.Close() }()
	if runErr := chmodSess.Run("chmod +x /tmp/chunk"); runErr != nil {
		return fmt.Errorf("chmod binary: %w", runErr)
	}
	return nil
}

// installChunkBinary installs /tmp/chunk to /usr/local/bin/chunk so it is on PATH.
func installChunkBinary(conn *sidecar.SSHConn) error {
	sess, err := conn.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer func() { _ = sess.Close() }()
	if runErr := sess.Run("sudo install -m 0755 /tmp/chunk /usr/local/bin/chunk"); runErr != nil {
		return fmt.Errorf("install to /usr/local/bin: %w", runErr)
	}
	return nil
}

// startRemoteDaemon starts "/tmp/chunk daemon" in the sidecar and waits up to
// 15s for it to become ready, polling by dialling through the SSH tunnel.
func startRemoteDaemon(conn *sidecar.SSHConn, port string) error {
	startSess, err := conn.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	// Fire-and-forget; ignore errors (daemon may already be running).
	_ = startSess.Run("/bin/sh -c 'nohup /tmp/chunk daemon --port " + port + " >/tmp/chunk-daemon.log 2>&1 &'")
	_ = startSess.Close()

	addr := "localhost:" + port
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, dialErr := conn.Dial("tcp", addr); dialErr == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("daemon on port %s did not become ready within 15s", port)
}
