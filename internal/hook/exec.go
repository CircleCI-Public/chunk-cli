package hook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ExecRunFlags holds parsed flags for exec run.
type ExecRunFlags struct {
	Name    string
	Cmd     string
	Timeout int
	FileExt string
	Staged  bool
	Always  bool
	NoCheck bool
	On      string
	Trigger string
	Limit   int
	Matcher string
}

// ExecCheckFlags holds parsed flags for exec check.
type ExecCheckFlags struct {
	Name         string
	Timeout      int
	FileExt      string
	Staged       bool
	Always       bool
	On           string
	Trigger      string
	Limit        int
	Matcher      string
	Cmd          string
	AllowMissing bool
}

// resolveExec merges flags with config to produce effective exec settings.
func resolveExec(cfg *ResolvedConfig, name, flagCmd, flagFileExt string, flagAlways bool, flagTimeout, flagLimit int) ExecConfig {
	yamlExec, ok := cfg.Execs[name]
	if !ok {
		yamlExec = ExecConfig{Timeout: 300}
	}

	command := flagCmd
	if command == "" {
		command = yamlExec.Command
	}
	if command == "" {
		command = fmt.Sprintf("echo 'No command configured for exec: %s'", name)
	}

	fileExt := flagFileExt
	if fileExt == "" {
		fileExt = yamlExec.FileExt
	}

	always := flagAlways || yamlExec.Always

	timeout := flagTimeout
	if timeout == 0 {
		timeout = yamlExec.Timeout
	}
	if timeout == 0 {
		timeout = 300
	}

	limit := flagLimit
	if limit == 0 {
		limit = yamlExec.Limit
	}

	return ExecConfig{
		Command: command,
		FileExt: fileExt,
		Always:  always,
		Timeout: timeout,
		Limit:   limit,
	}
}

// executeAndSave runs a command, writes the sentinel, and returns the verdict.
func executeAndSave(cfg *ResolvedConfig, execCfg ExecConfig, name string, staged bool) ExecCheckVerdict {
	startedAt := time.Now().UTC().Format(time.RFC3339)

	marker := ReadMarker(cfg.ProjectDir)
	sessionID := ""
	if marker != nil {
		sessionID = marker.SessionID
	}

	// Write pending sentinel
	if err := writeSentinel(cfg.SentinelDir, cfg.ProjectDir, name, SentinelData{
		Status:    "pending",
		StartedAt: startedAt,
		Project:   cfg.ProjectDir,
		SessionID: sessionID,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "chunk: warning: could not write sentinel for %q: %v\n", name, err)
	}

	// Build command with placeholder substitution
	command := execCfg.Command
	if strings.Contains(command, "{{CHANGED_FILES}}") || strings.Contains(command, "{{CHANGED_PACKAGES}}") {
		files := getChangedFiles(cfg.ProjectDir, staged, execCfg.FileExt)
		command = substitutePlaceholders(command, files)
	}

	// Execute command with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(execCfg.Timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = cfg.ProjectDir
	cmd.Env = cleanEnv()
	output, err := cmd.CombinedOutput()

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	status := verdictPass
	if exitCode != 0 {
		status = verdictFail
	}

	contentHash := computeFingerprint(cfg.ProjectDir, staged, execCfg.FileExt)

	sentinel := SentinelData{
		Status:            status,
		StartedAt:         startedAt,
		FinishedAt:        time.Now().UTC().Format(time.RFC3339),
		ExitCode:          exitCode,
		Command:           command,
		ConfiguredCommand: execCfg.Command,
		Output:            strings.TrimRight(string(output), "\n"),
		Project:           cfg.ProjectDir,
		SessionID:         sessionID,
		ContentHash:       contentHash,
	}
	if err := writeSentinel(cfg.SentinelDir, cfg.ProjectDir, name, sentinel); err != nil {
		fmt.Fprintf(os.Stderr, "chunk: warning: could not write sentinel for %q: %v\n", name, err)
	}

	return ExecCheckVerdict{Kind: status, Sentinel: &sentinel}
}

// RunExecRun executes a configured command, saves result as sentinel.
func RunExecRun(cfg *ResolvedConfig, flags ExecRunFlags) error {
	if !IsEnabled(flags.Name) {
		slog.Info("hook skipped", "name", flags.Name, "reason", "not enabled")
		return nil
	}

	execCfg := resolveExec(cfg, flags.Name, flags.Cmd, flags.FileExt, flags.Always, flags.Timeout, flags.Limit)

	// Skip if no changes (unless always)
	if !execCfg.Always {
		hasChanges, _ := detectChanges(cfg.ProjectDir, execCfg.FileExt, flags.Staged)
		if !hasChanges {
			startedAt := time.Now().UTC().Format(time.RFC3339)
			marker := ReadMarker(cfg.ProjectDir)
			sessionID := ""
			if marker != nil {
				sessionID = marker.SessionID
			}
			contentHash := computeFingerprint(cfg.ProjectDir, flags.Staged, execCfg.FileExt)
			if err := writeSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Name, SentinelData{
				Status:      "pass",
				StartedAt:   startedAt,
				FinishedAt:  time.Now().UTC().Format(time.RFC3339),
				ExitCode:    0,
				Output:      "No changed files. Skipped.",
				Skipped:     true,
				Project:     cfg.ProjectDir,
				SessionID:   sessionID,
				ContentHash: contentHash,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "chunk: warning: could not write sentinel for %q: %v\n", flags.Name, err)
			}
			return nil
		}
	}

	verdict := executeAndSave(cfg, execCfg, flags.Name, flags.Staged)

	if flags.NoCheck {
		return nil
	}

	if verdict.Kind == verdictFail {
		exitCode := 1
		if verdict.Sentinel != nil {
			exitCode = verdict.Sentinel.ExitCode
		}
		return fmt.Errorf("exec %q failed (exit %d)", flags.Name, exitCode)
	}
	return nil
}

// RunExecCheck reads a saved sentinel and enforces the result.
func RunExecCheck(cfg *ResolvedConfig, flags ExecCheckFlags, event map[string]interface{}) error {
	if !IsEnabled(flags.Name) {
		slog.Info("hook skipped", "name", flags.Name, "reason", "not enabled")
		return nil
	}

	execCfg := resolveExec(cfg, flags.Name, flags.Cmd, flags.FileExt, flags.Always, flags.Timeout, flags.Limit)
	limit := execCfg.Limit

	// Stop-event recursion guard
	if resp := guardStopEvent(event, limit); resp != nil {
		return emitResponse(*resp)
	}

	verdict := preEvaluateExec(cfg, event, execCfg, flags.Name, flags.Staged, flags.On, flags.Trigger, nil, "")

	return emitExecVerdict(cfg, flags, execCfg, verdict)
}

func emitExecVerdict(cfg *ResolvedConfig, flags ExecCheckFlags, execCfg ExecConfig, verdict ExecCheckVerdict) error {
	limit := execCfg.Limit
	name := flags.Name

	switch verdict.Kind {
	case "skip-trigger":
		return nil // allow
	case "skip-no-changes":
		return nil // allow
	case verdictMissing:
		if flags.AllowMissing {
			return nil // nothing was run, nothing to enforce
		}
		verdict = executeAndSave(cfg, execCfg, name, flags.Staged)
		return emitExecVerdict(cfg, flags, execCfg, verdict)
	case "pending":
		stillRunning := fmt.Sprintf("Exec %q is still running. Wait for completion before retrying.", name)
		sentinel := verdict.Sentinel
		timeout := execCfg.Timeout
		if sentinel == nil || sentinel.StartedAt == "" || timeout <= 0 {
			return emitResponse(blockNoCount(cfg.ProjectDir, stillRunning))
		}
		started, err := time.Parse(time.RFC3339, sentinel.StartedAt)
		if err != nil {
			return emitResponse(blockNoCount(cfg.ProjectDir, stillRunning))
		}
		elapsed := time.Since(started).Seconds()
		if elapsed <= float64(timeout) {
			return emitResponse(blockNoCount(cfg.ProjectDir, stillRunning))
		}
		runnerCmd := buildRunnerCommand(name, flags.Cmd, flags.Timeout, flags.FileExt, flags.Staged, flags.Always)
		reason := fmt.Sprintf(
			"Exec %q timed out after %ds (configured timeout: %ds).\n\n"+
				"The previous run may have an issue (infinite loop, deadlock, etc.). "+
				"Investigate and re-run:\n\n  %s",
			name, int(math.Round(elapsed)), timeout, runnerCmd)
		return emitResponse(blockWithLimit(cfg, name, limit, reason))
	case verdictPass:
		resetBlockCount(cfg.SentinelDir, cfg.ProjectDir, name)
		return nil // allow
	case verdictFail:
		sentinel := verdict.Sentinel
		cmd := execCfg.Command
		exitCode := 1
		output := "(no output)"
		if sentinel != nil && sentinel.Command != "" {
			cmd = sentinel.Command
		}
		if sentinel != nil && sentinel.ExitCode != 0 {
			exitCode = sentinel.ExitCode
		}
		if sentinel != nil && sentinel.Output != "" {
			output = sentinel.Output
		}
		reason := formatFailureReason(name, cmd, exitCode, output)
		return emitResponse(blockWithLimit(cfg, name, limit, reason))
	}

	return nil
}

func buildRunnerCommand(name, cmd string, _ int, _ string, staged, always bool) string {
	parts := []string{"chunk validate", name, "--no-check"}
	if cmd != "" {
		parts = append(parts, fmt.Sprintf("--cmd '%s'", cmd))
	}
	if staged {
		parts = append(parts, "--staged")
	}
	if always {
		parts = append(parts, "--always")
	}
	return strings.Join(parts, " ")
}

func formatFailureReason(name, command string, exitCode int, output string) string {
	header := fmt.Sprintf("Exec %q failed (exit %d, command: %s).", name, exitCode, command)
	if exitCode == 124 {
		header = fmt.Sprintf("Exec %q timed out (command: %s).", name, command)
	}
	return fmt.Sprintf("%s Fix the issues and retry.\n\nOutput:\n%s", header, output)
}

// cleanEnv returns a filtered copy of the current environment,
// removing CHUNK_HOOK_* vars to prevent contamination.
func cleanEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "CHUNK_HOOK_") {
			continue
		}
		env = append(env, e)
	}
	return env
}
