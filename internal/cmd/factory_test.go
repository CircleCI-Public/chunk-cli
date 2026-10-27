package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// putRecord drops rec where chunk factory looks for a run's record, so a test
// can check --continue against it without running a factory session.
func putRecord(t *testing.T, root string, rec factory.Record) {
	t.Helper()
	dataDir, err := config.RepoDataDir(root)
	assert.NilError(t, err)
	dir := filepath.Join(dataDir, "factory")
	assert.NilError(t, os.MkdirAll(dir, 0o700))
	b, err := json.Marshal(rec)
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(filepath.Join(dir, rec.RunID+".json"), b, 0o600))
}

// --remote is a supported, documented flag now that chunk factory pull can
// bring its work back, so it must no longer be hidden; --chunk-binary is for
// testing a dev build on the host and stays hidden.
func TestFactoryBuildRemoteFlagIsVisibleAndChunkBinaryStaysHidden(t *testing.T) {
	flags := newFactoryBuildCmd().Flags()

	remote := flags.Lookup("remote")
	assert.Assert(t, remote != nil)
	assert.Assert(t, !remote.Hidden, "--remote should be visible")

	chunkBinary := flags.Lookup("chunk-binary")
	assert.Assert(t, chunkBinary != nil)
	assert.Assert(t, chunkBinary.Hidden, "--chunk-binary should stay hidden")
}

// --max-attempts' help says what a round is, its default and why to change it.
func TestFactoryBuildMaxAttemptsHelp(t *testing.T) {
	usage := newFactoryBuildCmd().Flags().FlagUsages()
	assert.Assert(t, cmp.Contains(usage,
		"maximum rounds of implement, review and check. Raise for large changes; lower to cap cost (default 3)"))
}

// Running out of rounds says the cap was reached, the flag that raises it and,
// when the run committed work, the command that carries it on.
func TestExhaustedErrorNamesTheCapAndHowToGoFurther(t *testing.T) {
	committed := &chunkd.FactoryRun{Result: chunkd.ResultExhausted, Rounds: 3, Committed: true, Branch: "chunk/factory/add-flag/run-7"}
	ue := exhaustedError(committed)
	assert.Equal(t, ue.UserMessage(), "Checks still failed after 3 rounds: the run reached its cap (--max-attempts).")
	assert.Equal(t, ue.Suggestion(), `Raise the cap with --max-attempts <n>, or carry on with: chunk factory build --continue run-7 ["what to do differently"]`)

	// Without committed work there is nothing for --continue to pick up.
	ue = exhaustedError(&chunkd.FactoryRun{Result: chunkd.ResultExhausted, Rounds: 1})
	assert.Equal(t, ue.UserMessage(), "Checks still failed after 1 round: the run reached its cap (--max-attempts).")
	assert.Equal(t, ue.Suggestion(), "Raise the cap with --max-attempts <n>.")
}

// checkFactoryContinue refuses a run whose work is still on a per-run
// sidecar: there is nothing local yet to continue.
func TestCheckFactoryContinueRefusesARunOnASidecar(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := gitrepo.SetupGitRepo(t, "org", "repo")

	putRecord(t, root, factory.Record{
		RunID: "run-1", Prompt: "add a flag",
		Remote: &factory.RemoteRun{SidecarID: "sc-1", CommandID: "cmd-1", HostRepo: "/data/repo"},
	})
	err := checkFactoryContinue(root, "run-1")
	assert.ErrorContains(t, err, "is on a sidecar")

	putRecord(t, root, factory.Record{RunID: "run-2", Prompt: "add a flag"})
	assert.NilError(t, checkFactoryContinue(root, "run-2"))
}

// TestFactoryChecksOnlyToleratesMissingDefaultReviews guards against a
// mistyped --reviews silently running no reviews at all.
func TestFactoryChecksOnlyToleratesMissingDefaultReviews(t *testing.T) {
	workDir := t.TempDir()
	cfg := &config.ProjectConfig{Commands: []config.Command{{Name: "test", Run: "go test ./..."}}}

	prompts, commands, err := factoryChecks(workDir, "", cfg, false)
	assert.NilError(t, err, "a missing default directory is fine when there are commands")
	assert.Equal(t, len(prompts), 0)
	assert.Equal(t, len(commands), 1)

	_, _, err = factoryChecks(workDir, filepath.Join(workDir, "reviws"), cfg, false)
	assert.ErrorContains(t, err, "reviws")

	empty := filepath.Join(workDir, "empty")
	assert.NilError(t, os.Mkdir(empty, 0o755))
	_, _, err = factoryChecks(workDir, empty, cfg, false)
	assert.ErrorIs(t, err, review.ErrNoPrompts)
}

// With neither review prompts nor validation commands, factoryChecks points
// at bootstrap for prompts and at the config file directly for commands,
// since at this point .chunk/config.json already exists (loadFactoryConfig
// ensures that before factoryChecks is ever called) and a bootstrap rerun
// would skip the command-detection step rather than redo it.
func TestFactoryChecksNothingToCheckSuggestsBootstrapAndConfig(t *testing.T) {
	workDir := t.TempDir()
	cfg := &config.ProjectConfig{}

	_, _, err := factoryChecks(workDir, "", cfg, false)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "expected userError, got %T", err)
	assert.Equal(t, ue.UserMessage(), "Nothing to check the implementer's work with.")
	assert.Equal(t, ue.Suggestion(),
		"Run 'chunk factory bootstrap' to write review prompts to .chunk/reviews, or add validation commands directly to .chunk/config.json.")
}

func TestFactoryPrompt(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		stdin   string
		want    string
		wantErr string
	}{
		{name: "argument", args: []string{"add a flag"}, stdin: "ignored", want: "add a flag"},
		{name: "no argument reads stdin", stdin: "# Task\n\nadd a flag\n", want: "# Task\n\nadd a flag\n"},
		{name: "dash reads stdin", args: []string{"-"}, stdin: "# Task\n\nadd a flag\n", want: "# Task\n\nadd a flag\n"},
		{name: "empty implicit stdin", stdin: " \n", wantErr: "empty"},
		{name: "empty explicit stdin", args: []string{"-"}, stdin: " \n", wantErr: "empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, origin, err := factoryPrompt("chunk factory build", strings.NewReader(tc.stdin), tc.args, false)
			if tc.wantErr != "" {
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, got, tc.want)
			assert.Equal(t, origin, "")
		})
	}
}

// A sole argument naming an existing file is read as the prompt, wherever
// the file is, and its path is returned as the origin.
func TestFactoryPromptReadsAFileArgument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.md")
	assert.NilError(t, os.WriteFile(path, []byte("# Task\n\nadd a flag\n"), 0o644))

	got, origin, err := factoryPrompt("chunk factory build", strings.NewReader(""), []string{path}, false)
	assert.NilError(t, err)
	assert.Equal(t, got, "# Task\n\nadd a flag\n")
	assert.Equal(t, origin, path)
}

// A word with no whitespace that is not an existing file, and has no
// extension chunk treats as a spec file, is literal prompt text, as always.
func TestFactoryPromptTreatsAnUnknownMissingWordAsLiteralText(t *testing.T) {
	got, origin, err := factoryPrompt("chunk factory build", strings.NewReader(""), []string{"deploy-api"}, false)
	assert.NilError(t, err)
	assert.Equal(t, got, "deploy-api")
	assert.Equal(t, origin, "")
}

// A quoted sentence stays literal even when it mentions a file by name,
// since it has whitespace.
func TestFactoryPromptQuotedSentenceMentioningAFileStaysLiteral(t *testing.T) {
	got, origin, err := factoryPrompt("chunk factory build", strings.NewReader(""), []string{"fix README.md typos"}, false)
	assert.NilError(t, err)
	assert.Equal(t, got, "fix README.md typos")
	assert.Equal(t, origin, "")
}

// A missing path with a spec-like extension is an error, not literal text:
// that is a far more likely mistyped path than a prompt.
func TestFactoryPromptRefusesAMissingMarkdownPath(t *testing.T) {
	_, _, err := factoryPrompt("chunk factory build", strings.NewReader(""), []string{"missing-spec.md"}, false)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), "missing-spec.md: file not found.")
	assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk factory build < file.md"), ue.Suggestion())
	assert.Equal(t, ue.UserExitCode(), ExitBadArgs)
}

func TestFactoryPromptRefusesADirectory(t *testing.T) {
	dir := t.TempDir()
	_, _, err := factoryPrompt("chunk factory build", strings.NewReader(""), []string{dir}, false)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), dir+" is a directory.")
	assert.Equal(t, ue.UserExitCode(), ExitBadArgs)
}

func TestFactoryPromptRefusesABinaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.md")
	assert.NilError(t, os.WriteFile(path, []byte("binary\x00data"), 0o644))
	_, _, err := factoryPrompt("chunk factory build", strings.NewReader(""), []string{path}, false)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), path+" looks like a binary file.")
	assert.Equal(t, ue.UserExitCode(), ExitBadArgs)
}

func TestFactoryPromptRefusesAnOversizeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.txt")
	assert.NilError(t, os.WriteFile(path, make([]byte, maxPromptFileSize+1), 0o644))
	_, _, err := factoryPrompt("chunk factory build", strings.NewReader(""), []string{path}, false)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.Contains(ue.UserMessage(), "too large"), ue.UserMessage())
	assert.Equal(t, ue.UserExitCode(), ExitBadArgs)
}

// A prompt argument with a file redirected to stdin would silently drop the
// file's prompt, as in --log run.log < prompt.md, so it is refused. Empty files
// and non-files are what scripts inherit as stdin, so they are not.
func TestFactoryPromptRefusesAnArgumentAndARedirectedFile(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.md")
	assert.NilError(t, os.WriteFile(promptFile, []byte("# Task\n\nadd a flag\n"), 0o644))
	emptyFile := filepath.Join(dir, "empty")
	assert.NilError(t, os.WriteFile(emptyFile, nil, 0o644))
	open := func(path string) *os.File {
		f, err := os.Open(path)
		assert.NilError(t, err)
		t.Cleanup(func() { _ = f.Close() })
		return f
	}

	_, _, err := factoryPrompt("chunk factory build", open(promptFile), []string{"/tmp/run.log"}, true)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), `Got the prompt "/tmp/run.log" as an argument and another prompt on stdin.`)
	assert.Equal(t, ue.Suggestion(), "--log takes no file. To log to /tmp/run.log, write --log-file /tmp/run.log.")
	assert.Equal(t, ue.UserExitCode(), ExitBadArgs)

	// A stray word that happens to end in a spec-like extension, as in
	// --log run.txt < prompt.md, is still the specific "got it twice"
	// mistake, not a "file not found" about a path that was never meant as
	// one: redirected stdin is checked before the argument is read as a file.
	_, _, err = factoryPrompt("chunk factory build", open(promptFile), []string{"run.txt"}, true)
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), `Got the prompt "run.txt" as an argument and another prompt on stdin.`)
	assert.Equal(t, ue.Suggestion(), "--log takes no file. To log to run.txt, write --log-file run.txt.")
	assert.Equal(t, ue.UserExitCode(), ExitBadArgs)

	_, _, err = factoryPrompt("chunk factory build", open(promptFile), []string{"add a flag"}, false)
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.Suggestion(), "Pass the prompt as the argument or on stdin, not both.")

	got, origin, err := factoryPrompt("chunk factory build", open(promptFile), []string{"-"}, false)
	assert.NilError(t, err)
	assert.Equal(t, got, "# Task\n\nadd a flag\n")
	assert.Equal(t, origin, "")

	got, origin, err = factoryPrompt("chunk factory build", open(emptyFile), []string{"add a flag"}, true)
	assert.NilError(t, err)
	assert.Equal(t, got, "add a flag")
	assert.Equal(t, origin, "")
}

func TestFactoryArgs(t *testing.T) {
	cmd := newFactoryBuildCmd()
	assert.NilError(t, cmd.Args(cmd, nil))
	assert.NilError(t, cmd.Args(cmd, []string{"add a flag"}))
	assert.NilError(t, cmd.Args(cmd, []string{"-"}))
	assert.ErrorContains(t, cmd.Args(cmd, []string{""}), "one argument or on stdin")
	assert.ErrorContains(t, cmd.Args(cmd, []string{"one", "two"}), "one argument or on stdin")
}

func TestKeepWorkHint(t *testing.T) {
	// A branch that starts at the developer's HEAD merges.
	clean := &chunkd.FactoryRun{Branch: "chunk/factory/run-1", Baseline: "abc", Head: "abc"}
	assert.Equal(t, keepWorkHint(clean), "Merge it with: git merge chunk/factory/run-1")

	// One that starts from their uncommitted work would collide with that work
	// in their checkout, so only the run's own changes are applied.
	dirty := &chunkd.FactoryRun{Branch: "chunk/factory/run-1", Baseline: "def", Head: "abc"}
	assert.Equal(t, keepWorkHint(dirty), "Apply it with: git diff --binary def chunk/factory/run-1 | git apply")
}

func TestFactoryLogPath(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := gitrepo.SetupGitRepo(t, "org", "repo")
	dataDir, err := config.RepoDataDir(root)
	assert.NilError(t, err)
	name := "20261002-150405-a1b2c3"
	byName := filepath.Join(dataDir, "factory", name+".log")
	for _, tc := range []struct {
		name string
		path string
		on   bool
		want string
	}{
		{name: "no log", want: ""},
		// The default is named here, so this command can show the log.
		{name: "default", on: true, want: byName},
		{name: "file without the switch", path: "/tmp/run.log", want: "/tmp/run.log"},
		{name: "file with the switch", path: "/tmp/run.log", on: true, want: "/tmp/run.log"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := factoryLogPath(root, tc.path, tc.on, name)
			assert.NilError(t, err)
			assert.Equal(t, got, tc.want)
		})
	}

	// The daemon does not share this process's working directory, so a
	// relative path is resolved here.
	got, err := factoryLogPath(root, "run.log", false, name)
	assert.NilError(t, err)
	wd, err := os.Getwd()
	assert.NilError(t, err)
	assert.Equal(t, got, filepath.Join(wd, "run.log"))
}

// TestFactoryLogFileTakesTheNextWord guards --log-file FILE, which an optional
// value would have read as a log at the default path and a prompt of FILE.
func TestFactoryLogFileTakesTheNextWord(t *testing.T) {
	for _, args := range [][]string{
		{"--log-file", "run.log", "add a flag"},
		{"--log-file=run.log", "add a flag"},
	} {
		cmd := newFactoryBuildCmd()
		assert.NilError(t, cmd.ParseFlags(args), "args %q", args)
		assert.Equal(t, cmd.Flags().Lookup("log-file").Value.String(), "run.log", "args %q", args)
		assert.DeepEqual(t, cmd.Flags().Args(), []string{"add a flag"})
	}
}

// TestTailLogCopiesTheWholeLogByTheTimeItStops guards --verbose's last lines:
// what the run wrote just before it ended is shown, not lost with the tail.
func TestTailLogCopiesTheWholeLogByTheTimeItStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	var out bytes.Buffer
	stop := tailLog(path, 0, &out)
	// The log does not exist when the tail starts; the run creates it.
	assert.NilError(t, os.WriteFile(path, []byte("start\nend\n"), 0o600))
	stop()
	assert.Equal(t, out.String(), "start\nend\n")
}

// TestTailLogSkipsWhatTheLogHeldBeforeTheRun guards --log-file <existing file>: the
// run appends to it, and the earlier runs' lines are not shown again.
func TestTailLogSkipsWhatTheLogHeldBeforeTheRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	assert.NilError(t, os.WriteFile(path, []byte("earlier run\n"), 0o600))
	from := logSize(path)
	assert.Equal(t, from, int64(len("earlier run\n")))
	assert.Equal(t, logSize(filepath.Join(t.TempDir(), "none.log")), int64(0))

	var out bytes.Buffer
	stop := tailLog(path, from, &out)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	assert.NilError(t, err)
	_, err = f.WriteString("this run\n")
	assert.NilError(t, err)
	assert.NilError(t, f.Close())
	stop()
	assert.Equal(t, out.String(), "this run\n")
}

// TestLogTailCopiesWholeLines guards --verbose's terminal output: each line of
// the run's log once, and never half of one the run is still writing.
func TestLogTailCopiesWholeLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	var out bytes.Buffer
	tail := &logTail{w: &out, path: path}

	tail.follow() // not created yet
	assert.Equal(t, out.String(), "")

	assert.NilError(t, os.WriteFile(path, []byte("one\ntw"), 0o600))
	tail.follow()
	assert.Equal(t, out.String(), "one\n")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	assert.NilError(t, err)
	_, err = f.WriteString("o\nthree\n")
	assert.NilError(t, err)
	assert.NilError(t, f.Close())
	tail.follow()
	tail.follow()
	assert.Equal(t, out.String(), "one\ntwo\nthree\n")
}

// TestLogTailKeepsSidecarOutputOffTheTerminal guards --verbose: the log holds
// command output and agents' text from the sidecars, and its escape sequences
// are not passed on to the terminal.
func TestLogTailKeepsSidecarOutputOffTheTerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	assert.NilError(t, os.WriteFile(path, []byte("ok\tpkg \x1b]0;title\x07\x1b[31mFAIL\x1b[0m\r\n\u00e9\n"), 0o600))
	var out bytes.Buffer
	(&logTail{w: &out, path: path}).follow()
	// The escape bytes and the carriage return are gone; text, tabs, newlines
	// and non-ASCII letters stay.
	assert.Equal(t, out.String(), "ok\tpkg ]0;title[31mFAIL[0m\n\u00e9\n")
}

func TestPrintFactoryLeftoversPointsOnlyAtAKeptWorktree(t *testing.T) {
	collect := func(f *chunkd.FactoryRun) []string {
		var said []string
		status := func(_ iostream.Level, msg string) { said = append(said, msg) }
		printFactoryLeftovers(f, status, iostream.Streams{Out: io.Discard, Err: io.Discard})
		return said
	}

	kept := collect(&chunkd.FactoryRun{Worktree: "/wt/run-1"})
	assert.DeepEqual(t, kept, []string{"The work was not committed. It is in the worktree /wt/run-1"})

	// A run that failed before the implementer started had its empty worktree
	// removed; pointing at it would send the reader to a path that is gone.
	assert.Check(t, cmp.Len(collect(&chunkd.FactoryRun{Worktree: "/wt/run-1", WorktreeRemoved: true}), 0))
}

// A summary longer than a status line is cut on a character, not a byte: a
// cut inside a multi-byte character prints as a replacement character.
func TestOneLineSummaryCutsOnACharacter(t *testing.T) {
	long := strings.Repeat("é", 200)

	got := oneLineSummary(long)

	assert.Assert(t, utf8.ValidString(got), got)
	assert.Equal(t, got, strings.Repeat("é", 157)+"...")
	assert.Equal(t, oneLineSummary("  a \n b  "), "a b")
}
