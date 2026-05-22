package validate

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// mockExecutor records calls and optionally returns an error.
type mockExecutor struct {
	calls []mockCall
	err   error // set to return this error on the Nth call (0=first)
	failN int // call index to fail on; -1 means never fail
}

type mockCall struct {
	name    string
	command string
	timeout int
}

func (e *mockExecutor) Execute(ctx context.Context, name, command string, timeoutSec int) error {
	callIdx := len(e.calls)
	e.calls = append(e.calls, mockCall{name: name, command: command, timeout: timeoutSec})
	if e.failN >= 0 && callIdx == e.failN {
		return e.err
	}
	return nil
}

func testStatus(buf *bytes.Buffer) iostream.StatusFunc {
	return func(_ iostream.Level, msg string) {
		fmt.Fprintln(buf, msg)
	}
}

func TestRunner_RunAll(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "install", Run: "npm install"},
		{Name: "test", Run: "npm test"},
	}}
	exec := &mockExecutor{failN: -1}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, exec, testStatus(&statusBuf), streams)
	assert.NilError(t, runner.RunAll(context.Background()))

	assert.Equal(t, len(exec.calls), 2)
	assert.Equal(t, exec.calls[0].name, "install")
	assert.Equal(t, exec.calls[0].command, "npm install")
	assert.Equal(t, exec.calls[1].name, "test")
	assert.Equal(t, exec.calls[1].command, "npm test")
	assert.Assert(t, strings.Contains(statusBuf.String(), "Running install"), "got: %s", statusBuf.String())
	assert.Assert(t, strings.Contains(statusBuf.String(), "Running test"), "got: %s", statusBuf.String())
}

func TestRunner_RunAll_NoCommands(t *testing.T) {
	cfg := &config.ProjectConfig{}
	exec := &mockExecutor{failN: -1}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, exec, testStatus(&statusBuf), streams)
	err := runner.RunAll(context.Background())
	assert.ErrorIs(t, err, ErrNotConfigured)
}

func TestRunner_RunAll_Failure(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "test", Run: "false"},
	}}
	exec := &mockExecutor{failN: 0, err: fmt.Errorf("test command failed with exit code 1")}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, exec, testStatus(&statusBuf), streams)
	err := runner.RunAll(context.Background())
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "test command failed"), "got: %v", err)
}

func TestRunner_RunAll_SkipsAfterFailure(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "install", Run: "false"},
		{Name: "test", Run: "echo should-not-run"},
		{Name: "lint", Run: "echo should-not-run-either"},
	}}
	exec := &mockExecutor{failN: 0, err: fmt.Errorf("install command failed with exit code 1")}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, exec, testStatus(&statusBuf), streams)
	err := runner.RunAll(context.Background())
	assert.Assert(t, err != nil)
	assert.Equal(t, len(exec.calls), 1, "expected only install to run")
	assert.Assert(t, strings.Contains(statusBuf.String(), "test: skipped"), "got: %s", statusBuf.String())
	assert.Assert(t, strings.Contains(statusBuf.String(), "lint: skipped"), "got: %s", statusBuf.String())
}

func TestRunner_RunAll_SingleCommand(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "test", Run: "echo ok"},
	}}
	exec := &mockExecutor{failN: -1}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, exec, testStatus(&statusBuf), streams)
	assert.NilError(t, runner.RunAll(context.Background()))
	assert.Equal(t, len(exec.calls), 1)
	assert.Equal(t, exec.calls[0].name, "test")
}

func TestRunner_RunNamed(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "install", Run: "npm install"},
		{Name: "test", Run: "npm test"},
	}}
	exec := &mockExecutor{failN: -1}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, exec, testStatus(&statusBuf), streams)
	assert.NilError(t, runner.RunNamed(context.Background(), "test"))

	assert.Equal(t, len(exec.calls), 1)
	assert.Equal(t, exec.calls[0].name, "test")
	assert.Equal(t, exec.calls[0].command, "npm test")
	assert.Assert(t, !strings.Contains(statusBuf.String(), "install"), "should not mention install; got: %s", statusBuf.String())
}

func TestRunner_RunNamed_NotFound(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "test", Run: "npm test"},
	}}
	exec := &mockExecutor{failN: -1}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, exec, testStatus(&statusBuf), streams)
	err := runner.RunNamed(context.Background(), "lint")
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), `"lint" not configured`), "got: %v", err)
	assert.Equal(t, len(exec.calls), 0)
}

func TestRunner_RunNamed_WithTimeout(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "lint", Run: "eslint .", Timeout: 60},
	}}
	exec := &mockExecutor{failN: -1}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, exec, testStatus(&statusBuf), streams)
	assert.NilError(t, runner.RunNamed(context.Background(), "lint"))
	assert.Equal(t, exec.calls[0].timeout, 60)
}

func TestRunner_RunInline(t *testing.T) {
	cfg := &config.ProjectConfig{}
	exec := &mockExecutor{failN: -1}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, exec, testStatus(&statusBuf), streams)
	assert.NilError(t, runner.RunInline(context.Background(), "my-cmd", "echo hello"))

	assert.Equal(t, len(exec.calls), 1)
	assert.Equal(t, exec.calls[0].name, "my-cmd")
	assert.Equal(t, exec.calls[0].command, "echo hello")
	assert.Equal(t, exec.calls[0].timeout, 0) // inline uses 0 timeout
}

func TestRunner_RunInline_DefaultName(t *testing.T) {
	cfg := &config.ProjectConfig{}
	exec := &mockExecutor{failN: -1}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, exec, testStatus(&statusBuf), streams)
	assert.NilError(t, runner.RunInline(context.Background(), "", "echo hello"))
	assert.Equal(t, exec.calls[0].name, "custom")
}

func TestRunner_DryRun(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "install", Run: "npm install"},
		{Name: "test", Run: "npm test"},
	}}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, nil, testStatus(&statusBuf), streams)
	assert.NilError(t, runner.DryRun(""))

	assert.Assert(t, strings.Contains(statusBuf.String(), "install: npm install"), "got: %s", statusBuf.String())
	assert.Assert(t, strings.Contains(statusBuf.String(), "test: npm test"), "got: %s", statusBuf.String())
}

func TestRunner_DryRun_NoCommands(t *testing.T) {
	cfg := &config.ProjectConfig{}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, nil, testStatus(&statusBuf), streams)
	err := runner.DryRun("")
	assert.ErrorIs(t, err, ErrNotConfigured)
}

func TestRunner_DryRun_Named(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "install", Run: "npm install"},
		{Name: "test", Run: "npm test"},
	}}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, nil, testStatus(&statusBuf), streams)
	assert.NilError(t, runner.DryRun("test"))

	assert.Assert(t, strings.Contains(statusBuf.String(), "test: npm test"), "got: %s", statusBuf.String())
	assert.Assert(t, !strings.Contains(statusBuf.String(), "install"), "should not mention install; got: %s", statusBuf.String())
}

func TestRunner_DryRun_Named_NotFound(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "test", Run: "npm test"},
	}}
	var statusBuf bytes.Buffer
	streams, _, _ := newTestStreams()

	runner := NewRunner(cfg, nil, testStatus(&statusBuf), streams)
	err := runner.DryRun("lint")
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), `"lint" not configured`), "got: %v", err)
}
