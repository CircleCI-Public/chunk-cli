package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// ErrNoFactory is returned when factory is not installed, so there is nothing
// for a planning session's Agent to hand a plan to.
var ErrNoFactory = errors.New("factory is not installed")

// ErrNoFactoryPlugin is returned when claude's f plugin, which gives it the
// factory MCP tools and factory.md's instructions, is not loaded.
var ErrNoFactoryPlugin = errors.New("factory's f plugin is not loaded")

// ErrFactoryMCP is returned when factory's MCP server is not connected, so
// claude has no factory tools to plan with.
var ErrFactoryMCP = errors.New("factory's MCP server is not connected")

// ErrNoFactoryDaemon is returned when the factory daemon, which hands an
// Agent its planning tasks, is not running.
var ErrNoFactoryDaemon = errors.New("the factory daemon is not running")

// factoryBinary is the factory command an Agent checks is installed.
const factoryBinary = "factory"

// daemonNotRunning is in what factory says when its daemon isn't running.
const daemonNotRunning = "factory daemon isn't running"

// Package variables so tests can shorten them.
var (
	agentInitTimeout  = 60 * time.Second
	stopInterruptWait = 10 * time.Second
	stopTerminateWait = 5 * time.Second
)

const (
	// factoryMCP is the name claude's init event reports the factory MCP
	// server under.
	factoryMCP = "plugin:f:factory"
	// sourcegraphTools are Sourcegraph's read tools, for reading code
	// outside the repository.
	sourcegraphTools = "mcp__sg__code_finder,mcp__sg__commit_search,mcp__sg__diff_search,mcp__sg__keyword_search," +
		"mcp__sg__list_files,mcp__sg__list_repos,mcp__sg__read_file"
	// agentTools is what an Agent allows claude to use without asking: what
	// a factory planning session uses. It reads the repository and writes
	// only the plan, except that it may detach HEAD at another commit. It is
	// enough to plan, not to build.
	agentTools = "Read,Grep,Glob,Agent,mcp__plugin_f_factory," +
		"Bash(factory daemon wait),Bash(factory plan claim *),Bash(factory daemon release *)," +
		"Bash(codegraph *)," + sourcegraphTools + "," +
		"Bash(git log *),Bash(git show *),Bash(git diff *),Bash(git merge-base *),Bash(git rev-parse *)," +
		"Bash(git switch --detach *)," +
		"Bash(gh pr view *)"
	// agentDeniedTools is what an Agent never lets claude use, whatever the
	// developer's own settings allow: planning edits plans through the plan
	// tools, never the working tree.
	agentDeniedTools = "Edit,Write,NotebookEdit"
	// buildTools is what a build session allows claude to use without
	// asking, on top of acceptEdits in its worktree. The project's own
	// commands are not among them: they run on its sidecar, with chunk.
	buildTools = "Read,Grep,Glob,Edit,Write,Agent,TodoWrite,mcp__plugin_f_factory," +
		"Bash(factory *),Bash(git *),Bash(gh *),Bash(chunk *),Bash(codegraph *)," + sourcegraphTools
	// chatTools is what a chat session may use: only tools
	// that read.
	chatTools = "Read,Grep,Glob," +
		"mcp__plugin_f_factory__plan_read,mcp__plugin_f_factory__plan_get," +
		"mcp__plugin_f_factory__plan_grep,mcp__plugin_f_factory__plan_toc," +
		"mcp__plugin_f_factory__plan_comments,mcp__plugin_f_factory__plan_list," +
		"mcp__plugin_f_factory__plan_blockers," + sourcegraphTools + "," +
		"Bash(git log *),Bash(git show *)," +
		"Bash(factory daemon flow show *),Bash(factory daemon claim list),Bash(factory daemon session list)"
	// chatDeniedTools also denies the git options that write a file,
	// which the git read patterns would otherwise match.
	chatDeniedTools = "Edit,Write,NotebookEdit," +
		"Bash(git log *--output*),Bash(git show *--output*)"
)

// chatPrompt is what a chat session is told it is for.
const chatPrompt = "You are a read-only chat about the developer's factory plans in this repository, in chunk watch. " +
	"You read and explain; you never write, plan, claim a plan or run factory daemon wait, whatever the factory plugin's instructions say about starting or claiming plans. " +
	"Each message may start with where the developer is: [plan] the plan they are looking at, [flow] its status strip (step › now › next), " +
	"or [flows] their open flows, one per line; these are as of that screen's last poll. " +
	"Answer from the plans (the plan tools), the repository, Sourcegraph, git log and git show, and factory daemon flow show, claim list and session list. " +
	"When the developer wants a plan changed, tell them which part to comment on and with what. Changing keys on a flow screen: " +
	"c comments on the highlighted part, s sends queued comments, e edits a part, ctrl+s approves or starts the plan, x abandons it, X deletes it. " +
	"On the Flows list, enter opens a plan and n starts a new one. Keep replies short."

// buildMessage is a build session's first message: the plan is approved, so
// the session claims it and does the build tasks factory hands it. It names
// the sidecar because buildTools leaves the project's commands out, and asks
// the session to pass that on because a builder never sees this message.
const buildMessage = "Plan %s is approved, and factory builds it in this session. " +
	"Claim it by running `factory plan claim %[1]s` in Bash, then run `factory daemon wait` in the background and do the tasks it hands out, through to the pull request. " +
	"Check the project with `chunk validate`: it runs the project's checks on the plan's sidecar, or one of them with `chunk validate NAME`. " +
	"For a fallback or a one-off command, run it on the sidecar too: `chunk sidecar sync`, then `chunk sidecar exec` in the workspace sync prints, /home/user/ and the repository's name written out, " +
	"such as `chunk sidecar exec --command sh -- -c \"cd /home/user/REPO && go test ./internal/factory/...\"`. " +
	"Local go, task, gofmt and the like are denied in this session, not asked about. " +
	"Tell every builder you send this. " +
	"Nobody reads your replies while you build, so keep them short, and do not ask the developer anything: " +
	"what needs their decision goes in the plan, as factory's tasks say."

// BuildMessage is the first message of a session started with ModeBuild to build
// factory plan planID.
func BuildMessage(planID string) string { return fmt.Sprintf(buildMessage, planID) }

const buildRetryMessage = "Plan %s's claim has been released. " +
	"Claim it by running `factory plan claim %[1]s` in Bash, then run `factory daemon wait` in the background and do the tasks it hands out, through to the pull request, as your first message said."

// BuildRetryMessage is what a build session that could not claim factory
// plan planID is sent once the claim is released.
func BuildRetryMessage(planID string) string { return fmt.Sprintf(buildRetryMessage, planID) }

// planMessage is what a new plan's planning session is first sent: the
// claim instructions, then the developer's request.
const planMessage = "This conversation plans a change through factory, as plan %s. " +
	"Claim it by running `factory plan claim %[1]s` in Bash, then run `factory daemon wait` in the background and do the planning tasks it hands out. " +
	"The plan is written with the plan tools, never in your replies; keep replies short. " +
	noChatQuestions +
	"The developer's request:\n\n%s"

// planRetryMessage is what a planning session that holds no claim on its
// plan is sent: the claim instructions alone, since nothing the developer
// writes reaches it.
const planRetryMessage = "Your claim on plan %s has been released. " +
	"Claim it again by running `factory plan claim %[1]s` in Bash, then run `factory daemon wait` in the background and do the planning tasks it hands out. " +
	noChatQuestions

// noChatQuestions keeps the planner's questions out of its replies, which
// nobody answers.
const noChatQuestions = "Nobody can answer you in chat: put every question for the developer in 3. Open Questions, never in a reply. "

// PlanMessage is a new plan's planning session's first message: claim plan
// planID, plan it through the daemon's tasks, and here is the request.
func PlanMessage(planID, request string) string { return fmt.Sprintf(planMessage, planID, request) }

// PlanRetryMessage is what a planning session that holds no claim on plan
// planID is sent: the claim instructions alone, since nothing the developer
// writes reaches it.
func PlanRetryMessage(planID string) string { return fmt.Sprintf(planRetryMessage, planID) }

// Mode is what a session may do.
type Mode int

const (
	// ModePlan plans: it reads the repository and writes only the plan.
	ModePlan Mode = iota
	// ModeBuild builds a plan: it may edit Dir and run what factory's build
	// tasks run.
	ModeBuild
	// ModeChat only reads; chatTools is what it may read with.
	ModeChat
)

// AgentConfig starts a long-lived factory session.
type AgentConfig struct {
	// Dir is the repository claude plans in, or for ModeBuild the worktree it
	// builds in.
	Dir string
	// The zero value plans.
	Mode Mode
	// Model is the model to plan with, or "" for the developer's default.
	Model string
	// PluginDir loads the f plugin from this directory instead of the
	// installed one, or "" for the installed one.
	PluginDir string
	// SessionID is the session to start or, with Resume, to reopen.
	SessionID string
	// Resume reopens SessionID with --resume rather than starting it with
	// --session-id.
	Resume bool
}

// AgentTurn is one reply claude gave, outside of what it said while working.
type AgentTurn struct {
	Reply   string
	CostUSD float64
	// ByTask is true when nothing was sent since the last reply and a
	// background task ended since then: claude started this turn on its own,
	// on a factory task's notification, rather than on a developer's message.
	ByTask bool
}

// AgentEvent is one thing an Agent reports. Exactly one field is set.
type AgentEvent struct {
	// Activity is a tool call claude made, or a piece of text it wrote.
	Activity *claudecode.Activity
	// TurnEnded is claude's reply ending a turn.
	TurnEnded *AgentTurn
	// Problem is something wrong with the session: claude exited without
	// being stopped.
	Problem error
}

// Agent is a long-lived `claude -p --input-format stream-json` session with
// factory's f plugin: to factory, a session like any other, claiming plans
// and running `factory daemon wait` as factory.md tells it to. Agent sends it
// the developer's messages and reports what it does.
//
// Events must be drained until it closes. Events queue rather than block
// claude, so a slow consumer never stalls the session, a Send or a Stop.
type Agent struct {
	sessionID string // as claude's init reported it; set before StartAgent returns

	cmd   *exec.Cmd
	stdin io.WriteCloser
	// sendMu serializes writes to stdin. It is not mu, so a write blocked on
	// a full pipe can't hold up Stop or the reader of claude's stdout.
	sendMu sync.Mutex
	events chan AgentEvent
	wake   chan struct{} // wakes forward when queue grows or closed is set
	exited chan struct{} // closed after cmd.Wait returns and the reader is flushed
	stderr tailWriter    // written by exec until cmd.Wait returns, read after

	mu sync.Mutex
	// All fields below are guarded by mu.
	queue                      []AgentEvent // reported, not yet on events
	closed, stopping           bool
	sentSinceResult, taskEnded bool
	initCh                     chan claudecode.Init // first init only

	stopOnce sync.Once
	stopErr  error
}

// agentArgs is the claude command line StartAgent runs, after the program
// name.
func agentArgs(cfg AgentConfig) []string {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"}
	if cfg.Resume {
		args = append(args, "--resume", cfg.SessionID)
	} else {
		args = append(args, "--session-id", cfg.SessionID)
	}
	// Not the developer's own mode: in -p, a tool the list does not allow is
	// denied rather than asked about.
	switch cfg.Mode {
	case ModeBuild:
		args = append(args, "--permission-mode", "acceptEdits", "--allowedTools", buildTools)
	case ModeChat:
		args = append(args, "--permission-mode", "default", "--allowedTools", chatTools,
			flagDisallowedTools, chatDeniedTools, "--append-system-prompt", chatPrompt)
	case ModePlan:
		fallthrough
	default:
		args = append(args, "--permission-mode", "default",
			"--allowedTools", agentTools, flagDisallowedTools, agentDeniedTools)
	}
	if cfg.PluginDir != "" {
		args = append(args, "--plugin-dir", cfg.PluginDir)
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	return args
}

// StartAgent starts a long-lived factory planning session in cfg.Dir with
// message as its first turn, and waits for claude's init event, checking
// factory's f plugin is loaded and its MCP server connected before returning.
// claude reports init only once it has a message to answer, so a session
// can't start without one.
//
// ctx is the session's lifetime, not just its start: cancelling it at any
// point, even long after StartAgent returns, stops the session.
func StartAgent(ctx context.Context, cfg AgentConfig, message string) (*Agent, error) {
	if strings.TrimSpace(message) == "" {
		return nil, errors.New("a session needs a first message")
	}
	if _, err := exec.LookPath(claudeBinary); err != nil {
		return nil, ErrNoClaude
	}
	if _, err := exec.LookPath(factoryBinary); err != nil {
		return nil, ErrNoFactory
	}

	a := &Agent{
		sessionID: cfg.SessionID,
		events:    make(chan AgentEvent),
		wake:      make(chan struct{}, 1),
		exited:    make(chan struct{}),
		initCh:    make(chan claudecode.Init, 1),
	}
	// Not CommandContext: cancelling ctx runs Stop, which signals claude
	// itself, so exec must not send a signal of its own as well.
	cmd := exec.Command(claudeBinary, agentArgs(cfg)...)
	cmd.Dir = cfg.Dir
	cmd.Env = append(os.Environ(), config.EnvChunkHooksDisabled+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open claude's stdin: %w", err)
	}
	a.stdin = stdin
	events := claudecode.NewEvents(a.on)
	cmd.Stdout = eventsWriter{events}
	cmd.Stderr = &a.stderr
	agentProcAttr(cmd)
	// A background command claude started can outlive it holding stdout
	// open; don't let that keep Wait from returning.
	cmd.WaitDelay = 5 * time.Second
	a.cmd = cmd

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}
	go a.forward()

	go func() {
		runErr := cmd.Wait()
		events.Flush()
		a.mu.Lock()
		if !a.stopping {
			a.report(AgentEvent{Problem: fmt.Errorf("claude exited (code %d): %s", exitCodeOf(runErr), a.stderr.tail())})
		}
		a.closed = true
		a.signal()
		a.mu.Unlock()
		close(a.exited)
	}()

	context.AfterFunc(ctx, func() { _ = a.Stop() })

	// fail stops the session StartAgent will not return, and drains its
	// events so forward can finish.
	fail := func(err error) (*Agent, error) {
		go func() {
			for range a.events {
			}
		}()
		_ = a.Stop()
		return nil, err
	}

	if err := a.Send(message); err != nil {
		if waitForExit(a.exited, time.Second) {
			return fail(fmt.Errorf("claude exited before it started: %s", a.stderr.tail()))
		}
		return fail(err)
	}

	select {
	case init := <-a.initCh:
		if err := checkFactoryInit(init); err != nil {
			return fail(err)
		}
		// The session claude reports is the one factory's hooks claim plans
		// for, so Claims matches on it rather than on what was asked for.
		if init.SessionID != "" {
			a.sessionID = init.SessionID
		}
	case <-ctx.Done():
		return fail(ctx.Err())
	case <-a.exited:
		return fail(fmt.Errorf("claude exited before it started: %s", a.stderr.tail()))
	case <-time.After(agentInitTimeout):
		return fail(fmt.Errorf("claude did not report its init within %s", agentInitTimeout))
	}

	return a, nil
}

// checkFactoryInit reports ErrNoFactoryPlugin or ErrFactoryMCP when init does
// not show a working factory setup. A connected MCP server does not mean the
// daemon is running: factory's MCP server connects either way, and its tools
// say when the daemon isn't.
func checkFactoryInit(init claudecode.Init) error {
	hasPlugin := false
	for _, p := range init.Plugins {
		if p == "f" {
			hasPlugin = true
			break
		}
	}
	if !hasPlugin {
		return fmt.Errorf("%w: install factory's f plugin, or set PluginDir", ErrNoFactoryPlugin)
	}
	if init.MCPServers[factoryMCP] != "connected" {
		return fmt.Errorf("%w: factory must be on PATH", ErrFactoryMCP)
	}
	return nil
}

// Send writes message to claude as a new user turn.
func (a *Agent) Send(message string) error {
	b, err := json.Marshal(stdinMessage{Type: "user", Message: stdinUserMessage{Role: "user", Content: message}})
	if err != nil {
		return fmt.Errorf("encode message for claude: %w", err)
	}
	b = append(b, '\n')
	// Marked sent before writing, so a reply that arrives as the write
	// finishes is not taken for a turn a task started.
	a.mu.Lock()
	a.sentSinceResult = true
	a.mu.Unlock()
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	if _, err := a.stdin.Write(b); err != nil {
		return fmt.Errorf("send to claude: %w", err)
	}
	return nil
}

// stdinMessage is one line of stream-json input: a user message. It is
// marshaled, never built by joining strings, so nothing in message can break
// out of it.
type stdinMessage struct {
	Type    string           `json:"type"`
	Message stdinUserMessage `json:"message"`
}

type stdinUserMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Events is every Activity, TurnEnded and Problem an Agent reports, in order.
// It is closed once, after the session ends.
func (a *Agent) Events() <-chan AgentEvent { return a.events }

// Claims is the plans the factory daemon holds a claim on for this session,
// by address, as `factory daemon claim list` reports them.
func (a *Agent) Claims(ctx context.Context) ([]string, error) {
	all, err := listClaims(ctx)
	if err != nil {
		return nil, err
	}
	var plans []string
	for _, c := range all {
		if c.Session == a.sessionID {
			plans = append(plans, c.Plan)
		}
	}
	return plans, nil
}

// claim is one entry of `factory daemon claim list`.
type claim struct {
	Plan    string `json:"plan"`
	Session string `json:"session"`
}

// listClaims is every claim the factory daemon holds. It returns
// ErrNoFactory when factory is not installed and ErrNoFactoryDaemon when its
// daemon is not running.
func listClaims(ctx context.Context) ([]claim, error) {
	if _, err := exec.LookPath(factoryBinary); err != nil {
		return nil, ErrNoFactory
	}
	cmd := exec.CommandContext(ctx, factoryBinary, "daemon", "claim", "list")
	// factory retries for a minute by default while the daemon isn't running;
	// fail at once instead, so the screen can say so.
	cmd.Env = append(os.Environ(), "FACTORY_DAEMON_NOT_RUNNING_RETRY=0s")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if strings.Contains(stderr, daemonNotRunning) {
				return nil, ErrNoFactoryDaemon
			}
			return nil, fmt.Errorf("list factory claims: %s", stderr)
		}
		return nil, fmt.Errorf("list factory claims: %w", err)
	}
	var all []claim
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, fmt.Errorf("read factory claims: %w", err)
	}
	return all, nil
}

// ErrUnknownSession is returned by SessionWorktree for a session factory's
// daemon does not hold, or holds with no worktree.
var ErrUnknownSession = errors.New("factory's daemon does not know the session")

// sessionUnknown is in what factory says for a session its daemon does not
// hold.
const sessionUnknown = "has no session"

// SessionWorktree is the worktree root factory's daemon has session working
// in, from `factory daemon session show SESSION`. It returns ErrNoFactory
// when factory is not installed, ErrNoFactoryDaemon when its daemon is not
// running, and ErrUnknownSession for a session it does not hold.
func SessionWorktree(ctx context.Context, session string) (string, error) {
	if _, err := exec.LookPath(factoryBinary); err != nil {
		return "", ErrNoFactory
	}
	cmd := exec.CommandContext(ctx, factoryBinary, "daemon", "session", "show", session)
	// As in listClaims: fail at once while the daemon isn't running.
	cmd.Env = append(os.Environ(), "FACTORY_DAEMON_NOT_RUNNING_RETRY=0s")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			switch {
			case strings.Contains(stderr, daemonNotRunning):
				return "", ErrNoFactoryDaemon
			case strings.Contains(stderr, sessionUnknown):
				return "", ErrUnknownSession
			}
			return "", fmt.Errorf("show factory session %s: %s", session, stderr)
		}
		return "", fmt.Errorf("show factory session %s: %w", session, err)
	}
	var s struct {
		Location struct {
			Dir string `json:"dir"`
		} `json:"location"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return "", fmt.Errorf("read factory session %s: %w", session, err)
	}
	if s.Location.Dir == "" {
		return "", ErrUnknownSession
	}
	return s.Location.Dir, nil
}

// CheckFactoryDaemon reports ErrNoFactory when factory is not installed and
// ErrNoFactoryDaemon when its daemon is not running, so there is nothing to
// hand an Agent planning tasks.
func CheckFactoryDaemon(ctx context.Context) error {
	_, err := listClaims(ctx)
	return err
}

// Stop ends the session: SIGINT, then stdin closed, then SIGTERM if claude has
// not exited, then SIGKILL for whatever is left in claude's process group,
// claude included if it still has not exited. It is safe to call more than
// once and from multiple goroutines; every call returns the same result.
func (a *Agent) Stop() error {
	a.stopOnce.Do(func() {
		a.mu.Lock()
		a.stopping = true
		a.mu.Unlock()
		a.stopErr = a.stop()
	})
	return a.stopErr
}

func (a *Agent) stop() error {
	if a.cmd.Process == nil {
		return nil
	}
	_ = interrupt(a.cmd.Process)
	_ = a.stdin.Close()
	exited := waitForExit(a.exited, stopInterruptWait)
	if !exited {
		_ = terminate(a.cmd.Process)
		exited = waitForExit(a.exited, stopTerminateWait)
	}
	// Kill the whole process group, even once claude has exited: a background
	// factory daemon wait that outlives claude, SIGKILLed or not, would hold
	// the session's wait and make the next one fail with a 409. The group
	// outlives claude only while something in it is still running, so its ID
	// is not reused before then.
	_ = killGroup(a.cmd.Process)
	if exited {
		return nil
	}
	<-a.exited
	return errors.New("claude did not stop after SIGINT and SIGTERM; killed it")
}

func waitForExit(exited <-chan struct{}, d time.Duration) bool {
	select {
	case <-exited:
		return true
	case <-time.After(d):
		return false
	}
}

// report queues ev for Events unless the session has ended. It never blocks,
// so holding mu across it is safe. The caller must hold mu.
func (a *Agent) report(ev AgentEvent) {
	if a.closed {
		return
	}
	a.queue = append(a.queue, ev)
	a.signal()
}

// signal wakes forward. The caller must hold mu.
func (a *Agent) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// forward moves queued events onto the events channel in order, without
// holding mu while the consumer is slow, and closes the channel once the
// session has ended and the queue is empty.
func (a *Agent) forward() {
	for {
		a.mu.Lock()
		for len(a.queue) == 0 && !a.closed {
			a.mu.Unlock()
			<-a.wake
			a.mu.Lock()
		}
		if len(a.queue) == 0 {
			a.mu.Unlock()
			close(a.events)
			return
		}
		ev := a.queue[0]
		a.queue = a.queue[1:]
		a.mu.Unlock()
		a.events <- ev
	}
}

// on turns claude's events into AgentEvents. It is called serially on the
// goroutine that reads claude's stdout; Send also touches this state, so it
// runs under mu.
func (a *Agent) on(e claudecode.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch e.Kind {
	case claudecode.KindInit:
		select {
		case a.initCh <- e.Init:
		default:
		}
	case claudecode.KindActivity:
		act := e.Activity
		a.report(AgentEvent{Activity: &act})
	case claudecode.KindTaskEnded:
		a.taskEnded = true
	case claudecode.KindTurnEnded:
		byTask := !a.sentSinceResult && a.taskEnded
		a.sentSinceResult, a.taskEnded = false, false
		a.report(AgentEvent{TurnEnded: &AgentTurn{Reply: e.Result.Result, CostUSD: e.Result.CostUSD, ByTask: byTask}})
	}
}

// eventsWriter feeds claude's stdout to an Events parser as it arrives.
type eventsWriter struct{ events *claudecode.Events }

func (w eventsWriter) Write(p []byte) (int, error) {
	w.events.Write(p)
	return len(p), nil
}

// exitCodeOf is cmd.Wait's error as an exit code, or -1 when err does not
// carry one (claude was killed by a signal, or never ran).
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// tailWriter keeps the last outputTail bytes written to it. A session can run
// for hours, so what claude wrote to stderr as it exited is what matters, not
// what it wrote first.
type tailWriter struct{ buf []byte }

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if over := len(w.buf) - outputTail; over > 0 {
		w.buf = append(w.buf[:0], w.buf[over:]...)
	}
	return len(p), nil
}

// tail is what w kept, trimmed, for an error message.
func (w *tailWriter) tail() string { return strings.TrimSpace(string(w.buf)) }
