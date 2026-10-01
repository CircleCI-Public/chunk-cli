package review

import (
	"fmt"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// PrimaryPoolName names the one-sidecar pool that hosts a detached review. It
// differs from PoolName so the primary's own pool of reviewers, which lives in
// the primary's checkout, never collides with the laptop's state.
const PrimaryPoolName = "review-primary"

// RunsDir is where detached runs keep their files on the primary, relative to
// the sidecar user's home.
const RunsDir = ".chunk-review"

// Files a detached run leaves in its run directory.
const (
	ResultFile = "review.json" // the --json report
	LogFile    = "review.err"  // progress and errors
	ExitFile   = "exit"        // exit code; absent while the run is going
	PidFile    = "pid"         // the run's shell; lets a reader tell a dead run from a live one
)

// InstallRelease is the shell that installs the latest released chunk into
// $HOME on a Linux sidecar. Used when no local binary is uploaded.
const InstallRelease = `case "$(uname -m)" in aarch64|arm64) A=arm64;; *) A=x86_64;; esac
curl -fsSL "https://github.com/CircleCI-Public/chunk-cli/releases/latest/download/chunk-cli_Linux_${A}.tar.gz" | tar -xz -C "$HOME" chunk`

// UploadInstall is the shell that installs a binary streamed on stdin, gzipped,
// as $HOME/chunk. It writes a new file and moves it into place, because writing
// over a chunk that an earlier detached review is still running fails with
// "text file busy".
const UploadInstall = `gunzip > "$HOME/chunk.new" && chmod +x "$HOME/chunk.new" && mv -f "$HOME/chunk.new" "$HOME/chunk"`

// Exit codes of the detach script.
const (
	// ExitNoReview means the chunk on the primary runs but has no review command.
	ExitNoReview = 64
	// ExitBadBinary means the chunk on the primary cannot be executed at all,
	// for example a build for another OS or architecture.
	ExitBadBinary = 66
)

// DetachSpec describes the review the primary sidecar runs.
type DetachSpec struct {
	RunID       string
	RepoPath    string // checkout on the primary
	OrgID       string
	PromptsDir  string // relative to RepoPath; empty for the default
	Parallelism int
	Model       string
	Timeout     time.Duration
	Image       string // snapshot for the primary's reviewer sidecars; empty for its config default
	DestroyPool bool   // delete the primary's reviewer sidecars when the review ends
	// Install is a shell fragment that puts chunk at $HOME/chunk, run before
	// the review starts. Empty when the binary is already there.
	Install string
}

// RunDir returns the run's directory as a path relative to the sidecar home.
func (s DetachSpec) RunDir() string { return RunsDir + "/" + s.RunID }

// DetachScript builds the shell script that starts the review on the primary
// and returns at once. The review runs under nohup and setsid so it outlives
// the exec that started it; its report, log and exit code land in the run
// directory, which is printed as the script's last line.
func DetachScript(s DetachSpec) string {
	args := []string{
		`"$HOME/chunk"`, "review", "--json",
		"--org-id", sidecar.ShellEscape(s.OrgID),
		"--parallelism", fmt.Sprint(s.Parallelism),
	}
	if s.Model != "" {
		args = append(args, "--model", sidecar.ShellEscape(s.Model))
	}
	if s.Timeout > 0 {
		args = append(args, "--timeout", sidecar.ShellEscape(s.Timeout.String()))
	}
	if s.Image != "" {
		args = append(args, "--image", sidecar.ShellEscape(s.Image))
	}
	if s.DestroyPool {
		args = append(args, "--destroy-pool")
	}
	if s.PromptsDir != "" {
		args = append(args, sidecar.ShellEscape(s.PromptsDir))
	}

	// The child shell inherits RUN, so its redirections can name the run
	// directory without this script quoting it into another layer of quotes.
	// The shell records its pid first, so a reader can tell a run that was
	// killed before it could write its exit code from one still going.
	inner := fmt.Sprintf(`echo $$ > "$RUN/%s"; %s > "$RUN/%s" 2> "$RUN/%s"; echo $? > "$RUN/%s"`,
		PidFile, strings.Join(args, " "), ResultFile, LogFile, ExitFile)

	var b strings.Builder
	b.WriteString("set -e\n")
	fmt.Fprintf(&b, "RUN=\"$HOME/%s\"\n", s.RunDir())
	b.WriteString("export RUN\nmkdir -p \"$RUN\"\n")
	if s.Install != "" {
		fmt.Fprintln(&b, s.Install)
	}
	// A chunk that cannot run, or a release older than 'chunk review', would
	// fail in the background where nobody is watching, so find out here, while
	// the caller can still be told. A missing or non-executable file, or shell
	// exit 126 or 127, means chunk cannot run; any other failure means it ran
	// without a review command.
	fmt.Fprintf(&b, "[ -x \"$HOME/chunk\" ] || exit %d\n", ExitBadBinary)
	b.WriteString("rc=0\n")
	b.WriteString("\"$HOME/chunk\" review --help >/dev/null 2>&1 || rc=$?\n")
	fmt.Fprintf(&b, "case $rc in 0) ;; 126|127) exit %d;; *) exit %d;; esac\n", ExitBadBinary, ExitNoReview)
	fmt.Fprintf(&b, "cd %s\n", sidecar.ShellEscape(s.RepoPath))
	fmt.Fprintf(&b, "nohup setsid sh -c %s >/dev/null 2>&1 </dev/null &\n", sidecar.ShellEscape(inner))
	b.WriteString("echo \"$RUN\"\n")
	return b.String()
}

// DetachEnv is the environment the primary's review runs with: the credentials
// its own chunk needs to create the reviewer sidecars and to run Claude on them.
func DetachEnv(circleCIToken string, opts Options) map[string]string {
	env := Env(opts.Credential, opts.BaseURL)
	env[config.EnvCircleCIToken] = circleCIToken
	return env
}

// Run states reported by ReadScript.
const (
	RunRunning = "running"
	RunDone    = "done"
	RunMissing = "missing"
	RunDied    = "died" // no exit code, and the run's process is gone
)

// ReadScript builds the shell script that reports a detached run: a STATUS
// line first, then the report once the run has finished or the tail of its log
// while it is still going.
func ReadScript(runDir string) string {
	return fmt.Sprintf(`cd %s 2>/dev/null || { echo "STATUS %s"; exit 0; }
if [ -f %s ]; then
  echo "STATUS %s $(cat %s)"
  cat %s
  echo
  echo "%s"
  tail -n 8 %s 2>/dev/null || true
else
  if [ -f %s ] && ! kill -0 "$(cat %s)" 2>/dev/null; then
    echo "STATUS %s"
  else
    echo "STATUS %s"
  fi
  tail -n 8 %s 2>/dev/null || true
fi
`, sidecar.ShellEscape(runDir), RunMissing,
		ExitFile, RunDone, ExitFile, ResultFile, logMarker, LogFile,
		PidFile, PidFile, RunDied, RunRunning, LogFile)
}

// logMarker separates a finished run's report from the tail of its log.
const logMarker = "STATUS-LOG"

// RunStatus is a detached run as ReadScript reported it.
type RunStatus struct {
	State    string // RunRunning, RunDone, RunMissing or RunDied
	ExitCode int    // meaningful when State is RunDone
	Body     string // the report when done, the log tail when running
	Log      string // the log tail when done
}

// ParseRead parses the output of ReadScript.
func ParseRead(out string) (RunStatus, error) {
	first, body, _ := strings.Cut(out, "\n")
	fields := strings.Fields(first)
	if len(fields) < 2 || fields[0] != "STATUS" {
		return RunStatus{}, fmt.Errorf("unexpected output from the primary sidecar: %q", first)
	}
	st := RunStatus{State: fields[1], Body: body}
	if report, log, ok := strings.Cut(body, "\n"+logMarker+"\n"); ok {
		st.Body, st.Log = report, log
	}
	switch st.State {
	case RunRunning, RunMissing, RunDied:
		return st, nil
	case RunDone:
		if len(fields) != 3 {
			return RunStatus{}, fmt.Errorf("the run finished without an exit code: %q", first)
		}
		if _, err := fmt.Sscanf(fields[2], "%d", &st.ExitCode); err != nil {
			return RunStatus{}, fmt.Errorf("parse exit code %q: %w", fields[2], err)
		}
		return st, nil
	}
	return RunStatus{}, fmt.Errorf("unknown run state %q", st.State)
}
