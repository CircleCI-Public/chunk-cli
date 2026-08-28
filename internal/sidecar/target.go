package sidecar

import (
	"context"
	"errors"
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// Target represents operations against one sidecar.
// It is the base unit that higher-level coordination, including pools, builds on.
type Target struct {
	Client       *circleci.Client
	SidecarID    string
	IdentityFile string
	AuthSock     string
	Workdir      string
	RetryOn404   bool
}

type WorkspaceNotFoundError struct {
	SidecarID string
	Path      string
}

func (e *WorkspaceNotFoundError) Error() string {
	if e.SidecarID != "" {
		return fmt.Sprintf("workspace directory not found on sidecar %s: %s", e.SidecarID, e.Path)
	}
	return fmt.Sprintf("workspace directory not found on sidecar: %s", e.Path)
}

type TargetUnavailableError struct {
	SidecarID string
	Err       error
}

func (e *TargetUnavailableError) Error() string {
	return fmt.Sprintf("sidecar %s unavailable: %v", e.SidecarID, e.Err)
}

func (e *TargetUnavailableError) Unwrap() error {
	return e.Err
}

func (t Target) ResolveWorkspace(ctx context.Context, cwd string) (string, error) {
	_, repo, _ := gitremote.DetectOrgAndRepo(cwd)
	return ResolveWorkspace(ctx, t.Workdir, repo)
}

func (t Target) Sync(ctx context.Context, cwd string, useBundle bool, status iostream.StatusFunc) error {
	if useBundle {
		return BundleSync(ctx, t.Client, t.SidecarID, t.IdentityFile, t.AuthSock, t.Workdir, cwd, t.RetryOn404, status)
	}
	return syncCheckout(ctx, t.Client, t.SidecarID, t.IdentityFile, t.AuthSock, t.Workdir, cwd, status)
}

func (t Target) ExecRunner(ctx context.Context, cwd string, envVars map[string]string, streams iostream.Streams) (func(context.Context, string) (string, string, int, error), string, error) {
	dest, err := t.ResolveWorkspace(ctx, cwd)
	if err != nil {
		return nil, "", err
	}
	onOutput := func(stream string, data []byte) {
		w := streams.Out
		if stream == circleci.StreamStderr {
			w = streams.Err
		}
		_, _ = w.Write(data)
	}
	execFn := func(ctx context.Context, script string) (string, string, int, error) {
		result, err := t.Client.Exec(ctx, t.SidecarID, "sh", []string{"-c", script}, envVars, onOutput)
		if err != nil {
			return "", "", 0, err
		}
		return "", "", result.ExitCode, nil
	}
	return execFn, dest, nil
}

func (t Target) ReadyExecRunner(ctx context.Context, cwd string, envVars map[string]string, streams iostream.Streams) (func(context.Context, string) (string, string, int, error), string, error) {
	execFn, dest, err := t.ExecRunner(ctx, cwd, envVars, streams)
	if err != nil {
		return nil, "", err
	}
	_, _, exitCode, err := execFn(ctx, "test -d "+ShellEscape(dest))
	if err != nil {
		return nil, "", &TargetUnavailableError{SidecarID: t.SidecarID, Err: fmt.Errorf("check workspace: %w", err)}
	}
	if exitCode != 0 {
		return nil, "", &WorkspaceNotFoundError{SidecarID: t.SidecarID, Path: dest}
	}
	return execFn, dest, nil
}

func syncCheckout(ctx context.Context, client *circleci.Client, sidecarID, identityFile, authSock, workdir, cwd string, status iostream.StatusFunc) error {
	session, err := OpenSession(ctx, client, sidecarID, identityFile, authSock, false)
	if err != nil {
		return err
	}

	org, repo, err := gitremote.DetectOrgAndRepo(cwd)
	if err != nil {
		return &NoOriginRemoteError{Err: err}
	}

	repoPath, err := ResolveWorkspace(ctx, workdir, repo)
	if err != nil {
		return err
	}

	if err := persistWorkspace(ctx, repoPath); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not save workspace: %v", err))
	}

	err = syncWorkspace(ctx, status, org, repo, repoPath, session)
	if err == nil {
		status(iostream.LevelDone, "Synced")
		return nil
	}
	if !errors.Is(err, errApplyFailed) {
		return err
	}

	status(iostream.LevelWarn, fmt.Sprintf("Local %s/%s drifted from remote: %s (%s) - attempting clean",
		org, repo, repoPath, err))

	if result, err := ExecOverSSH(ctx, session, "rm -rf "+ShellEscape(repoPath), nil, nil); err != nil {
		return fmt.Errorf("sync: rm %s: %w", repoPath, err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("sync: rm %s: %s", repoPath, result.Stderr)
	}

	if err := syncWorkspace(ctx, status, org, repo, repoPath, session); err != nil {
		return fmt.Errorf("sync retry: %w", err)
	}

	status(iostream.LevelDone, "Synced")
	return nil
}
