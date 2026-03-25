package hook

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initGitRepo creates a git repo with an initial commit and a modified file.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %s\n%s", args, err, out)
		}
	}

	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")

	// Create an uncommitted change
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestExecRunWithChanges(t *testing.T) {
	t.Setenv("CHUNK_HOOK_ENABLE", "1")
	projDir := initGitRepo(t)
	sentDir := t.TempDir()

	cfg := &ResolvedConfig{
		SentinelDir: sentDir,
		ProjectDir:  projDir,
		Execs:       map[string]ExecConfig{},
		Tasks:       map[string]TaskConfig{},
		Triggers:    map[string][]string{},
	}

	err := RunExecRun(cfg, ExecRunFlags{
		Name:   "tests",
		Cmd:    "echo ok",
		Always: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify sentinel was written with content hash and session fields
	s := readSentinel(sentDir, projDir, "tests")
	if s == nil {
		t.Fatal("expected sentinel to exist")
	}
	if s.Status != "pass" {
		t.Fatalf("expected pass, got %q", s.Status)
	}
	if s.ContentHash == "" {
		t.Fatal("expected content hash to be set")
	}
	if s.ConfiguredCommand != "echo ok" {
		t.Fatalf("expected configured command, got %q", s.ConfiguredCommand)
	}
}

func TestExecRunSkipsWhenNoChanges(t *testing.T) {
	t.Setenv("CHUNK_HOOK_ENABLE", "1")
	dir := t.TempDir()

	// Init a clean git repo with no uncommitted changes
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s\n%s", args, err, out)
		}
	}

	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")

	sentDir := t.TempDir()
	cfg := &ResolvedConfig{
		SentinelDir: sentDir,
		ProjectDir:  dir,
		Execs:       map[string]ExecConfig{},
		Tasks:       map[string]TaskConfig{},
		Triggers:    map[string][]string{},
	}

	err := RunExecRun(cfg, ExecRunFlags{
		Name: "tests",
		Cmd:  "echo should-not-run",
	})
	if err != nil {
		t.Fatal(err)
	}

	s := readSentinel(sentDir, dir, "tests")
	if s == nil {
		t.Fatal("expected sentinel")
	}
	if !s.Skipped {
		t.Fatal("expected skipped=true when no changes")
	}
	if s.ContentHash == "" {
		t.Fatal("expected content hash even for skipped")
	}
}

func TestExecCheckPassesSentinel(t *testing.T) {
	t.Setenv("CHUNK_HOOK_ENABLE", "1")
	projDir := initGitRepo(t)
	sentDir := t.TempDir()

	cfg := &ResolvedConfig{
		SentinelDir: sentDir,
		ProjectDir:  projDir,
		Execs: map[string]ExecConfig{
			"tests": {Command: "echo ok", Timeout: 300, Always: true},
		},
		Tasks:    map[string]TaskConfig{},
		Triggers: map[string][]string{},
	}

	// Run first to create sentinel
	err := RunExecRun(cfg, ExecRunFlags{Name: "tests", Always: true})
	if err != nil {
		t.Fatal(err)
	}

	// Check should pass
	err = RunExecCheck(cfg, ExecCheckFlags{Name: "tests", Always: true}, nil)
	if err != nil {
		t.Fatalf("expected pass, got: %v", err)
	}
}

func TestExecCheckBlocksOnFail(t *testing.T) {
	t.Setenv("CHUNK_HOOK_ENABLE", "1")
	projDir := initGitRepo(t)
	sentDir := t.TempDir()

	cfg := &ResolvedConfig{
		SentinelDir: sentDir,
		ProjectDir:  projDir,
		Execs: map[string]ExecConfig{
			"tests": {Command: "exit 1", Timeout: 300, Always: true},
		},
		Tasks:    map[string]TaskConfig{},
		Triggers: map[string][]string{},
	}

	// Run the failing command (no-check so it saves)
	_ = RunExecRun(cfg, ExecRunFlags{Name: "tests", Always: true, NoCheck: true})

	// Check should block
	err := RunExecCheck(cfg, ExecCheckFlags{Name: "tests", Always: true}, nil)
	if err == nil {
		t.Fatal("expected block error")
	}
	if !isBlockError(err) {
		t.Fatalf("expected BlockError, got: %T", err)
	}
}

func TestBlockLimitEnforcement(t *testing.T) {
	t.Setenv("CHUNK_HOOK_ENABLE", "1")
	projDir := initGitRepo(t)
	sentDir := t.TempDir()

	cfg := &ResolvedConfig{
		SentinelDir: sentDir,
		ProjectDir:  projDir,
		Execs: map[string]ExecConfig{
			"tests": {Command: "exit 1", Timeout: 300, Always: true, Limit: 2},
		},
		Tasks:    map[string]TaskConfig{},
		Triggers: map[string][]string{},
	}

	// Run failing command
	_ = RunExecRun(cfg, ExecRunFlags{Name: "tests", Always: true, NoCheck: true})

	// First two checks should block
	for i := 0; i < 2; i++ {
		err := RunExecCheck(cfg, ExecCheckFlags{Name: "tests", Always: true, Limit: 2}, nil)
		if err == nil {
			t.Fatalf("check %d: expected block", i+1)
		}
		if !isBlockError(err) {
			t.Fatalf("check %d: expected BlockError, got: %T", i+1, err)
		}
	}

	// Third check should auto-allow (limit exceeded)
	err := RunExecCheck(cfg, ExecCheckFlags{Name: "tests", Always: true, Limit: 2}, nil)
	if err != nil {
		t.Fatalf("expected auto-allow after limit, got: %v", err)
	}
}

func TestBlockCountResetsOnPass(t *testing.T) {
	t.Setenv("CHUNK_HOOK_ENABLE", "1")
	projDir := initGitRepo(t)
	sentDir := t.TempDir()

	cfg := &ResolvedConfig{
		SentinelDir: sentDir,
		ProjectDir:  projDir,
		Execs: map[string]ExecConfig{
			"tests": {Command: "exit 1", Timeout: 300, Always: true, Limit: 5},
		},
		Tasks:    map[string]TaskConfig{},
		Triggers: map[string][]string{},
	}

	// Fail then check to increment block count
	_ = RunExecRun(cfg, ExecRunFlags{Name: "tests", Always: true, NoCheck: true})
	_ = RunExecCheck(cfg, ExecCheckFlags{Name: "tests", Always: true, Limit: 5}, nil)

	count := readBlockCount(sentDir, projDir, "tests")
	if count != 1 {
		t.Fatalf("expected block count 1, got %d", count)
	}

	// Now change the config to a passing command and re-run
	cfg.Execs["tests"] = ExecConfig{Command: "echo ok", Timeout: 300, Always: true, Limit: 5}
	_ = RunExecRun(cfg, ExecRunFlags{Name: "tests", Always: true, NoCheck: true})
	_ = RunExecCheck(cfg, ExecCheckFlags{Name: "tests", Always: true, Limit: 5}, nil)

	count = readBlockCount(sentDir, projDir, "tests")
	if count != 0 {
		t.Fatalf("expected block count reset to 0, got %d", count)
	}
}

func TestTriggerMatching(t *testing.T) {
	t.Run("matches trigger pattern", func(t *testing.T) {
		event := map[string]interface{}{
			"tool_input": map[string]interface{}{
				"command": "git commit -m 'test'",
			},
		}
		if !matchesTrigger(event, []string{"git commit"}) {
			t.Fatal("expected trigger to match")
		}
	})

	t.Run("does not match unrelated command", func(t *testing.T) {
		event := map[string]interface{}{
			"tool_input": map[string]interface{}{
				"command": "echo hello",
			},
		}
		if matchesTrigger(event, []string{"git commit"}) {
			t.Fatal("expected trigger not to match")
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		event := map[string]interface{}{
			"tool_input": map[string]interface{}{
				"command": "GIT COMMIT -m 'test'",
			},
		}
		if !matchesTrigger(event, []string{"git commit"}) {
			t.Fatal("expected case-insensitive match")
		}
	})

	t.Run("empty patterns match everything", func(t *testing.T) {
		event := map[string]interface{}{}
		if !matchesTrigger(event, nil) {
			t.Fatal("empty patterns should match everything")
		}
	})
}

func TestScopeFilePathFiltering(t *testing.T) {
	t.Run("matches project path", func(t *testing.T) {
		raw := map[string]interface{}{
			"tool_input": map[string]interface{}{
				"file_path": "/project/src/main.go",
			},
		}
		result := matchesProject("/project", raw)
		if result != "match" {
			t.Fatalf("expected match, got %q", result)
		}
	})

	t.Run("no paths returns no-paths", func(t *testing.T) {
		raw := map[string]interface{}{}
		result := matchesProject("/project", raw)
		if result != "no-paths" {
			t.Fatalf("expected no-paths, got %q", result)
		}
	})

	t.Run("mismatched path", func(t *testing.T) {
		raw := map[string]interface{}{
			"tool_input": map[string]interface{}{
				"file_path": "/other-project/main.go",
			},
		}
		result := matchesProject("/project", raw)
		if result != "mismatch" {
			t.Fatalf("expected mismatch, got %q", result)
		}
	})

	t.Run("extracts path from command", func(t *testing.T) {
		raw := map[string]interface{}{
			"tool_input": map[string]interface{}{
				"command": "cat /project/main.go | head",
			},
		}
		result := matchesProject("/project", raw)
		if result != "match" {
			t.Fatalf("expected match from command path, got %q", result)
		}
	})
}

func TestScopeSessionAwareDeactivate(t *testing.T) {
	dir := t.TempDir()

	// Activate with session A
	inputA := fmt.Sprintf(`{"session_id":"sess-A","tool_input":{"file_path":"%s/main.go"}}`, dir)
	if err := ActivateScope(dir, strings.NewReader(inputA)); err != nil {
		t.Fatal(err)
	}

	// Try to deactivate with session B — should be no-op
	if err := DeactivateScope(dir, strings.NewReader(`{"session_id":"sess-B"}`)); err != nil {
		t.Fatal(err)
	}

	marker := ReadMarker(dir)
	if marker == nil {
		t.Fatal("marker should still exist (different session)")
	}
	if marker.SessionID != "sess-A" {
		t.Fatalf("expected sess-A, got %q", marker.SessionID)
	}

	// Deactivate with session A — should succeed
	if err := DeactivateScope(dir, strings.NewReader(`{"session_id":"sess-A"}`)); err != nil {
		t.Fatal(err)
	}

	marker = ReadMarker(dir)
	if marker != nil {
		t.Fatal("marker should be removed after same-session deactivate")
	}
}

func TestScopeTTLExpiry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHUNK_HOOK_MARKER_TTL_MS", "1") // 1ms TTL

	// Activate with session A
	inputA := fmt.Sprintf(`{"session_id":"sess-A","tool_input":{"file_path":"%s/main.go"}}`, dir)
	if err := ActivateScope(dir, strings.NewReader(inputA)); err != nil {
		t.Fatal(err)
	}

	// Wait for TTL to expire
	time.Sleep(5 * time.Millisecond)

	// Session B should be able to reclaim
	inputB := fmt.Sprintf(`{"session_id":"sess-B","tool_input":{"file_path":"%s/main.go"}}`, dir)
	if err := ActivateScope(dir, strings.NewReader(inputB)); err != nil {
		t.Fatal(err)
	}

	marker := ReadMarker(dir)
	if marker == nil {
		t.Fatal("expected marker after reclaim")
	}
	if marker.SessionID != "sess-B" {
		t.Fatalf("expected sess-B after reclaim, got %q", marker.SessionID)
	}
}

func TestSentinelSessionStaleness(t *testing.T) {
	s := &SentinelData{Status: "pass", StartedAt: "2024-01-01T00:00:00Z", SessionID: "old-session"}

	result := evaluateSentinel(s, "new-session", "")
	if result.Kind != "missing" {
		t.Fatalf("expected missing for stale session, got %q", result.Kind)
	}
}

func TestSentinelContentStaleness(t *testing.T) {
	s := &SentinelData{Status: "pass", StartedAt: "2024-01-01T00:00:00Z", ContentHash: "abc123"}

	result := evaluateSentinel(s, "", "different-hash")
	if result.Kind != "missing" {
		t.Fatalf("expected missing for stale content, got %q", result.Kind)
	}
}

func TestTaskResultParsing(t *testing.T) {
	sentDir := t.TempDir()
	projDir := "/test/project"

	t.Run("parses allow decision", func(t *testing.T) {
		path := SentinelPath(sentDir, projDir, "review")
		data := `{"decision":"allow","reason":"looks good"}`
		_ = os.WriteFile(path, []byte(data), 0o644)

		s := readTaskResult(sentDir, projDir, "review", "")
		if s == nil {
			t.Fatal("expected sentinel")
		}
		if s.Status != "pass" {
			t.Fatalf("expected pass for allow decision, got %q", s.Status)
		}
	})

	t.Run("parses block decision", func(t *testing.T) {
		path := SentinelPath(sentDir, projDir, "review2")
		data := `{"decision":"block","reason":"found issues"}`
		_ = os.WriteFile(path, []byte(data), 0o644)

		s := readTaskResult(sentDir, projDir, "review2", "")
		if s == nil {
			t.Fatal("expected sentinel")
		}
		if s.Status != "fail" {
			t.Fatalf("expected fail for block decision, got %q", s.Status)
		}
		if s.Details != "found issues" {
			t.Fatalf("expected reason, got %q", s.Details)
		}
		if s.RawResult == "" {
			t.Fatal("expected raw result to be preserved")
		}
	})

	t.Run("returns nil for invalid JSON", func(t *testing.T) {
		path := SentinelPath(sentDir, projDir, "bad")
		_ = os.WriteFile(path, []byte("not json"), 0o644)

		s := readTaskResult(sentDir, projDir, "bad", "")
		if s != nil {
			t.Fatal("expected nil for invalid JSON")
		}
	})
}

func TestSyncGroupSentinel(t *testing.T) {
	t.Setenv("CHUNK_HOOK_ENABLE", "1")
	projDir := initGitRepo(t)
	sentDir := t.TempDir()

	cfg := &ResolvedConfig{
		SentinelDir: sentDir,
		ProjectDir:  projDir,
		Execs: map[string]ExecConfig{
			"tests": {Command: "echo ok", Timeout: 300, Always: true},
			"lint":  {Command: "echo ok", Timeout: 300, Always: true},
		},
		Tasks:    map[string]TaskConfig{},
		Triggers: map[string][]string{},
	}

	specs := []CommandSpec{
		{Type: "exec", Name: "tests"},
		{Type: "exec", Name: "lint"},
	}

	// Run both exec commands
	_ = RunExecRun(cfg, ExecRunFlags{Name: "tests", Always: true, NoCheck: true})
	_ = RunExecRun(cfg, ExecRunFlags{Name: "lint", Always: true, NoCheck: true})

	// Sync check should pass
	err := RunSyncCheck(cfg, SyncCheckFlags{
		Specs:  specs,
		Always: true,
	}, nil)
	if err != nil {
		t.Fatalf("expected sync to pass, got: %v", err)
	}
}

func TestSyncGroupPartialPass(t *testing.T) {
	t.Setenv("CHUNK_HOOK_ENABLE", "1")
	projDir := initGitRepo(t)
	sentDir := t.TempDir()

	cfg := &ResolvedConfig{
		SentinelDir: sentDir,
		ProjectDir:  projDir,
		Execs: map[string]ExecConfig{
			"tests": {Command: "echo ok", Timeout: 300, Always: true},
			"lint":  {Command: "exit 1", Timeout: 300, Always: true},
		},
		Tasks:    map[string]TaskConfig{},
		Triggers: map[string][]string{},
	}

	specs := []CommandSpec{
		{Type: "exec", Name: "tests"},
		{Type: "exec", Name: "lint"},
	}

	// Run tests (pass) and lint (fail)
	_ = RunExecRun(cfg, ExecRunFlags{Name: "tests", Always: true, NoCheck: true})
	_ = RunExecRun(cfg, ExecRunFlags{Name: "lint", Always: true, NoCheck: true})

	// Sync check should block
	err := RunSyncCheck(cfg, SyncCheckFlags{
		Specs:  specs,
		Always: true,
	}, nil)
	if err == nil {
		t.Fatal("expected sync to block when lint fails")
	}
	if !isBlockError(err) {
		t.Fatalf("expected BlockError, got: %T", err)
	}
}

func TestSyncOnFailRetry(t *testing.T) {
	t.Setenv("CHUNK_HOOK_ENABLE", "1")
	projDir := initGitRepo(t)
	sentDir := t.TempDir()

	cfg := &ResolvedConfig{
		SentinelDir: sentDir,
		ProjectDir:  projDir,
		Execs: map[string]ExecConfig{
			"tests": {Command: "echo ok", Timeout: 300, Always: true},
			"lint":  {Command: "exit 1", Timeout: 300, Always: true},
		},
		Tasks:    map[string]TaskConfig{},
		Triggers: map[string][]string{},
	}

	specs := []CommandSpec{
		{Type: "exec", Name: "tests"},
		{Type: "exec", Name: "lint"},
	}

	// Run both
	_ = RunExecRun(cfg, ExecRunFlags{Name: "tests", Always: true, NoCheck: true})
	_ = RunExecRun(cfg, ExecRunFlags{Name: "lint", Always: true, NoCheck: true})

	// First sync check with retry mode
	_ = RunSyncCheck(cfg, SyncCheckFlags{
		Specs:  specs,
		Always: true,
		OnFail: "retry",
	}, nil)

	// In retry mode, tests should still be marked as passed
	group := readGroupSentinel(sentDir, projDir, specs)
	// After failure, the group sentinel should have tests in passed (retry preserves)
	// but lint removed
	hasTests := false
	for _, p := range group.Passed {
		if p == "exec:tests" {
			hasTests = true
		}
	}
	if !hasTests {
		t.Fatal("retry mode should preserve already-passed specs")
	}
}

func TestEvaluateSentinelStates(t *testing.T) {
	tests := []struct {
		name     string
		sentinel *SentinelData
		session  string
		hash     string
		want     string
	}{
		{"nil sentinel", nil, "", "", "missing"},
		{"pass", &SentinelData{Status: "pass"}, "", "", "pass"},
		{"fail", &SentinelData{Status: "fail"}, "", "", "fail"},
		{"pending", &SentinelData{Status: "pending"}, "", "", "pending"},
		{"session match", &SentinelData{Status: "pass", SessionID: "s1"}, "s1", "", "pass"},
		{"session mismatch", &SentinelData{Status: "pass", SessionID: "s1"}, "s2", "", "missing"},
		{"hash match", &SentinelData{Status: "pass", ContentHash: "h1"}, "", "h1", "pass"},
		{"hash mismatch", &SentinelData{Status: "pass", ContentHash: "h1"}, "", "h2", "missing"},
		{"pending ignores hash", &SentinelData{Status: "pending"}, "", "h1", "pending"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := evaluateSentinel(tt.sentinel, tt.session, tt.hash)
			if result.Kind != tt.want {
				t.Fatalf("got %q, want %q", result.Kind, tt.want)
			}
		})
	}
}

func TestFileExtensionFiltering(t *testing.T) {
	projDir := initGitRepo(t)

	// Add a .ts file
	_ = os.WriteFile(filepath.Join(projDir, "app.ts"), []byte("console.log('hi')\n"), 0o644)

	// Detect changes filtered to .go files
	hasGoChanges, _ := detectChanges(projDir, ".go", false)
	if !hasGoChanges {
		t.Fatal("expected .go changes to be detected")
	}

	// Detect changes filtered to .py files (should be none)
	hasPyChanges, _ := detectChanges(projDir, ".py", false)
	if hasPyChanges {
		t.Fatal("expected no .py changes")
	}

	// Detect changes for .ts files
	hasTsChanges, _ := detectChanges(projDir, ".ts", false)
	if !hasTsChanges {
		t.Fatal("expected .ts changes to be detected")
	}
}

func TestComputeFingerprint(t *testing.T) {
	projDir := initGitRepo(t)

	fp1 := computeFingerprint(projDir, false, "")
	if fp1 == "" {
		t.Fatal("expected non-empty fingerprint")
	}

	// Same state should give same fingerprint
	fp2 := computeFingerprint(projDir, false, "")
	if fp1 != fp2 {
		t.Fatalf("fingerprints should be deterministic: %q != %q", fp1, fp2)
	}

	// Modify a file should change fingerprint
	_ = os.WriteFile(filepath.Join(projDir, "main.go"), []byte("package main\n\nvar x = 1\n"), 0o644)
	fp3 := computeFingerprint(projDir, false, "")
	if fp3 == fp1 {
		t.Fatal("fingerprint should change after file modification")
	}
}

func TestGuardStopEvent(t *testing.T) {
	t.Run("not stop event returns nil", func(t *testing.T) {
		event := map[string]interface{}{}
		resp := guardStopEvent(event, 0)
		if resp != nil {
			t.Fatal("expected nil for non-stop event")
		}
	})

	t.Run("stop event with limit=0 auto-allows", func(t *testing.T) {
		event := map[string]interface{}{"stop_hook_active": true}
		resp := guardStopEvent(event, 0)
		if resp == nil {
			t.Fatal("expected auto-allow for stop event with limit=0")
		}
		if resp.Action != "allow" {
			t.Fatalf("expected allow, got %q", resp.Action)
		}
	})

	t.Run("stop event with limit>0 defers", func(t *testing.T) {
		event := map[string]interface{}{"stop_hook_active": true}
		resp := guardStopEvent(event, 3)
		if resp != nil {
			t.Fatal("expected nil (deferred to blockWithLimit)")
		}
	})
}

func TestCommandValidation(t *testing.T) {
	t.Setenv("CHUNK_HOOK_ENABLE", "1")
	projDir := initGitRepo(t)
	sentDir := t.TempDir()

	cfg := &ResolvedConfig{
		SentinelDir: sentDir,
		ProjectDir:  projDir,
		Execs: map[string]ExecConfig{
			"tests": {Command: "go test ./...", Timeout: 300, Always: true},
		},
		Tasks:    map[string]TaskConfig{},
		Triggers: map[string][]string{},
	}

	// Write a sentinel with a different command (simulating --cmd bypass)
	_ = writeSentinel(sentDir, projDir, "tests", SentinelData{
		Status:            "pass",
		StartedAt:         time.Now().UTC().Format(time.RFC3339),
		FinishedAt:        time.Now().UTC().Format(time.RFC3339),
		ExitCode:          0,
		ConfiguredCommand: "true", // different command
		ContentHash:       computeFingerprint(projDir, false, ""),
		Project:           projDir,
	})

	// Check should treat as missing due to command mismatch
	err := RunExecCheck(cfg, ExecCheckFlags{Name: "tests", Always: true}, nil)
	if err == nil {
		t.Fatal("expected block due to command mismatch")
	}
	if !isBlockError(err) {
		t.Fatalf("expected BlockError, got: %T", err)
	}
}

