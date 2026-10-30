package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

// chatIdle is how long a chat session runs with no message
// and no turn before the daemon stops it; the next message resumes it.
var chatIdle = 30 * time.Minute

// chats holds each repository's chat, made on its first
// message.
type chats struct {
	ctx context.Context // the daemon's
	// rcfg is the daemon's, read when a chat is made, so a test that
	// sets its seams after the daemon is built still has them used.
	rcfg    *PlanConfig
	mu      sync.Mutex
	byRoot  map[string]*chat
	stopped bool
}

// newChats makes the daemon's chats, stopping them all once
// ctx is done.
func newChats(ctx context.Context, rcfg *PlanConfig) *chats {
	cs := &chats{ctx: ctx, rcfg: rcfg, byRoot: make(map[string]*chat)}
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			cs.stopAll()
		}()
	}
	return cs
}

// get is root's chat, made if it has none, or nil once the daemon is
// shutting down. Making one starts nothing: its session starts on its first
// message.
func (cs *chats) get(root string) *chat {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.stopped {
		return nil
	}
	if c, ok := cs.byRoot[root]; ok {
		return c
	}
	start := cs.rcfg.StartChat
	if start == nil {
		start = func(ctx context.Context, cfg factory.AgentConfig, message string) (AgentSession, error) {
			a, err := factory.StartAgent(ctx, cfg, message)
			if err != nil {
				return nil, err
			}
			return a, nil
		}
	}
	open := cs.rcfg.OpenChatRecord
	if open == nil {
		open = func(root string) (SessionRecord, error) {
			s, err := factory.OpenChatSession(root)
			if err != nil {
				return nil, err
			}
			return s, nil
		}
	}
	c := &chat{
		root: root,
		run:  newRunner(cs.ctx, factory.AgentConfig{Mode: factory.ModeChat}, start),
		open: open,
	}
	c.run.onReply = c.armIdle
	cs.byRoot[root] = c
	return c
}

// lookup is root's chat, nil when it has none.
func (cs *chats) lookup(root string) *chat {
	if cs == nil {
		return nil
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.byRoot[root]
}

// stopAll stops every chat's session and closes its record. It is
// safe to call more than once.
func (cs *chats) stopAll() {
	if cs == nil {
		return
	}
	cs.mu.Lock()
	cs.stopped = true
	all := make([]*chat, 0, len(cs.byRoot))
	for _, c := range cs.byRoot {
		all = append(all, c)
	}
	cs.mu.Unlock()
	for _, c := range all {
		c.disarmIdle()
		c.run.stop()
	}
}

// chat is one repository's chat session: a runner in
// ModeChat in root, stopped when idle and resumed on the next message.
type chat struct {
	root string
	run  *runner
	open func(root string) (SessionRecord, error)

	// mu orders a message's note in the log with its send, and keeps the
	// idle stop from racing a send.
	mu sync.Mutex

	// idleMu guards idle. It is not mu: armIdle runs on the runner's
	// forwarding goroutine, which must not wait on a send, as a claude whose
	// output is not read can stop reading what it is sent.
	idleMu sync.Mutex
	idle   *time.Timer
}

// send notes msg's text in the log and sends it, after its context, to the
// session, starting or resuming one as needed. What goes wrong is a problem
// in the log, so every window shows it.
func (c *chat) send(msg chunkd.ChatMessage) (chunkd.SessionLog, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disarmIdle()
	c.run.note(chunkd.SessionLogMessage, msg.Text)
	if !c.run.opened() {
		rec, err := c.open(c.root)
		if err == nil {
			_, err = c.run.open(rec)
		}
		if err != nil {
			// The next message tries the record again.
			c.run.note(chunkd.SessionLogProblem, chatProblem(err))
			return c.run.view(-1), nil
		}
	}
	text := msg.Text
	if msg.Context != "" {
		text = msg.Context + "\n\n" + msg.Text
	}
	if _, err := c.run.send(text, func() (string, error) { return c.root, nil }); err != nil {
		c.run.note(chunkd.SessionLogProblem, chatProblem(err))
	}
	return c.run.view(-1), nil
}

// chatProblem is what the conversation says when a message could not
// reach the chat's session, saying how to fix it.
func chatProblem(err error) string {
	switch {
	case errors.Is(err, factory.ErrChatOpen):
		return "the repository's chat is open in another chunk daemon; stop that daemon, then send again"
	case errors.Is(err, errRunnerStopped):
		return "the chunk daemon is shutting down; send the message again once it has restarted"
	}
	return sessionStartProblem(err, "chat").Error()
}

// armIdle starts the idle timer afresh: the runner's onReply. It does not
// take mu (see idleMu).
func (c *chat) armIdle(bool) {
	c.idleMu.Lock()
	defer c.idleMu.Unlock()
	if c.idle != nil {
		c.idle.Reset(chatIdle)
		return
	}
	c.idle = time.AfterFunc(chatIdle, c.idleStop)
}

func (c *chat) disarmIdle() {
	c.idleMu.Lock()
	defer c.idleMu.Unlock()
	if c.idle != nil {
		c.idle.Stop()
	}
}

// idleStop stops the session, not the runner, unless it is mid-turn: the
// log, record and session ID stay, and the next message resumes it. A stop
// it asked for adds nothing to the log.
func (c *chat) idleStop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run.view(-1).Busy {
		return
	}
	if a, live := c.run.live(); live {
		if err := a.Stop(); err != nil {
			log.Printf("chunkd: chat %s: stop the idle session: %v", c.root, err)
		}
	}
}

// chatRoot is the root a chat request names, canonicalized, or
// why it names none: it must be absolute, with no "..".
func chatRoot(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("root required")
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("root %q is not an absolute path", raw)
	}
	if slices.Contains(strings.Split(filepath.ToSlash(raw), "/"), "..") {
		return "", fmt.Errorf("root %q has a \"..\" in it", raw)
	}
	return config.CanonicalProjectRoot(filepath.Clean(raw)), nil
}
