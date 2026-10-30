package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

// chatTestRoot is the root the chat tests that start no real session
// use: with the session and its record faked, nothing reads it.
const chatTestRoot = "/repo"

// chatFakes starts fake chat sessions and opens a record in memory
// per root.
type chatFakes struct {
	*fakeStarts
	recMu   sync.Mutex
	records map[string]*memRecord
	openErr error
}

func newChatFakes() *chatFakes {
	return &chatFakes{fakeStarts: &fakeStarts{}, records: make(map[string]*memRecord)}
}

func (f *chatFakes) config() PlanConfig {
	return PlanConfig{
		StartChat: f.start,
		OpenChatRecord: func(root string) (SessionRecord, error) {
			f.recMu.Lock()
			err := f.openErr
			f.recMu.Unlock()
			if err != nil {
				return nil, err
			}
			return f.record(root).open()
		},
	}
}

func (f *chatFakes) setOpenErr(err error) {
	f.recMu.Lock()
	defer f.recMu.Unlock()
	f.openErr = err
}

// record is root's record, made empty if it has none.
func (f *chatFakes) record(root string) *memRecord {
	f.recMu.Lock()
	defer f.recMu.Unlock()
	r, ok := f.records[root]
	if !ok {
		r = &memRecord{}
		f.records[root] = r
	}
	return r
}

// newTestChats is chats whose sessions and records f fakes,
// stopped once the test ends.
func newTestChats(t *testing.T, f *chatFakes) *chats {
	t.Helper()
	rcfg := f.config()
	cs := newChats(context.Background(), &rcfg)
	t.Cleanup(cs.stopAll)
	return cs
}

// inChatBubble runs body in a synctest bubble with chats made
// in it, so its idle timers run on the bubble's clock. Their sessions are
// stopped as the bubble ends.
func inChatBubble(t *testing.T, f *chatFakes, body func(t *testing.T, c *chat)) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rcfg := f.config()
		cs := newChats(ctx, &rcfg)
		body(t, cs.get(chatTestRoot))
	})
}

func chatMsg(text string) chunkd.ChatMessage {
	return chunkd.ChatMessage{Root: chatTestRoot, Text: text}
}

// mustSend sends msg to c, which answers with its log.
func mustSend(t *testing.T, c *chat, msg chunkd.ChatMessage) chunkd.SessionLog {
	t.Helper()
	log, err := c.send(msg)
	assert.NilError(t, err)
	return log
}

func TestChatStartsOnTheFirstMessageOnly(t *testing.T) {
	f := newChatFakes()
	cs := newTestChats(t, f)
	c := cs.get(chatTestRoot)
	assert.Equal(t, f.sessions(), 0)

	log := mustSend(t, c, chunkd.ChatMessage{Root: chatTestRoot, Text: "what's left?", Context: "[plan] p1 t"})
	assert.Check(t, log.Busy)
	cfgs := f.configs()
	assert.Assert(t, cmp.Len(cfgs, 1))
	assert.Check(t, cmp.Equal(cfgs[0].Mode, factory.ModeChat))
	assert.Check(t, cmp.Equal(cfgs[0].Dir, chatTestRoot))
	assert.Check(t, cmp.Equal(f.first(0), "[plan] p1 t\n\nwhat's left?"))
	assert.Check(t, cmp.DeepEqual(f.record(chatTestRoot).recordedIDs(), []string{cfgs[0].SessionID}))

	mustSend(t, c, chatMsg("and after that?"))
	assert.Check(t, cmp.Len(f.configs(), 1))
	assert.Check(t, cmp.DeepEqual(f.session(0).messages(), []string{"and after that?"}))
}

func TestChatLogsTheDevelopersWords(t *testing.T) {
	f := newChatFakes()
	cs := newTestChats(t, f)
	mustSend(t, cs.get(chatTestRoot), chunkd.ChatMessage{Root: chatTestRoot, Text: "what's left?", Context: "[plan] p1 t"})
	log := cs.get(chatTestRoot).run.view(0)
	assert.Check(t, cmp.DeepEqual(log.Events, []chunkd.SessionLogEvent{
		{Seq: 0, Kind: chunkd.SessionLogMessage, Text: "what's left?"},
	}))

	// With no context, the session is sent the text alone.
	mustSend(t, cs.get("/other"), chunkd.ChatMessage{Root: "/other", Text: "hi"})
	assert.Check(t, cmp.Equal(f.first(1), "hi"))
}

func TestChatRecordOpenFailureIsAProblem(t *testing.T) {
	f := newChatFakes()
	f.setOpenErr(factory.ErrChatOpen)
	cs := newTestChats(t, f)
	c := cs.get(chatTestRoot)

	mustSend(t, c, chatMsg("hi"))
	log := c.run.view(0)
	assert.Assert(t, cmp.Len(log.Events, 2))
	assert.Check(t, cmp.DeepEqual(log.Events[0], chunkd.SessionLogEvent{Seq: 0, Kind: chunkd.SessionLogMessage, Text: "hi"}))
	assert.Check(t, cmp.Equal(log.Events[1].Kind, chunkd.SessionLogProblem))
	assert.Check(t, cmp.Contains(log.Events[1].Text, "stop that daemon"))
	assert.Check(t, !log.Busy)
	assert.Check(t, cmp.Len(f.configs(), 0))

	// The next message opens the record again.
	f.setOpenErr(nil)
	log = mustSend(t, c, chatMsg("again"))
	assert.Check(t, cmp.Equal(f.sessions(), 1))
	assert.Check(t, log.Busy)
}

func TestChatStartFailureIsAProblem(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root, err := filepath.EvalSymlinks(initRepo(t))
	assert.NilError(t, err)
	f := newChatFakes()
	startErr := fmt.Errorf("%w: install factory's f plugin", factory.ErrNoFactoryPlugin)
	f.setErrs(startErr, nil)
	startTestDaemonWithPlans(t, f.config())

	_, err = chunkd.SendChat(chunkd.ChatMessage{Root: root, Text: "hi"})
	assert.NilError(t, err, "a start failure still answers 200")
	log, err := chunkd.FetchChat(root, 0)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(log.Events, 2))
	assert.Check(t, cmp.DeepEqual(log.Events[1], chunkd.SessionLogEvent{
		Seq: 1, Kind: chunkd.SessionLogProblem, Text: sessionStartProblem(startErr, "chat").Error(),
	}))
	assert.Check(t, !log.Busy)
}

func TestChatStopsWhenIdleAndResumes(t *testing.T) {
	f := newChatFakes()
	inChatBubble(t, f, func(t *testing.T, c *chat) {
		mustSend(t, c, chatMsg("what's left?"))
		f.session(0).reply("One question is open.")
		synctest.Wait()
		before := c.run.view(0)
		assert.Assert(t, !before.Busy)

		time.Sleep(chatIdle + time.Second)
		synctest.Wait()
		assert.Assert(t, f.session(0).wasStopped())
		assert.Check(t, cmp.DeepEqual(c.run.view(0), before), "the log is kept as it was")

		mustSend(t, c, chatMsg("and now?"))
		cfgs := f.configs()
		assert.Assert(t, cmp.Len(cfgs, 2))
		assert.Check(t, cfgs[1].Resume)
		assert.Check(t, cmp.Equal(cfgs[1].SessionID, cfgs[0].SessionID))
	})
}

func TestChatDoesNotStopMidTurn(t *testing.T) {
	f := newChatFakes()
	inChatBubble(t, f, func(t *testing.T, c *chat) {
		mustSend(t, c, chatMsg("what's left?"))
		time.Sleep(chatIdle + time.Second)
		synctest.Wait()
		assert.Check(t, !f.session(0).wasStopped())

		f.session(0).reply("Done.")
		synctest.Wait()
		time.Sleep(chatIdle + time.Second)
		synctest.Wait()
		assert.Check(t, f.session(0).wasStopped())
	})
}

func TestChatMessageResetsTheIdleTimer(t *testing.T) {
	f := newChatFakes()
	inChatBubble(t, f, func(t *testing.T, c *chat) {
		mustSend(t, c, chatMsg("what's left?"))
		f.session(0).reply("One question is open.")
		synctest.Wait()

		time.Sleep(chatIdle - time.Minute)
		mustSend(t, c, chatMsg("and now?"))
		f.session(0).reply("Nothing.")
		synctest.Wait()
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		assert.Check(t, !f.session(0).wasStopped())
	})
}

func TestChatsMakeOnePerRoot(t *testing.T) {
	f := newChatFakes()
	cs := newTestChats(t, f)
	var want []string
	var wg sync.WaitGroup
	for i := range 20 {
		text := fmt.Sprintf("m%d", i)
		want = append(want, text)
		wg.Go(func() { mustSend(t, cs.get(chatTestRoot), chatMsg(text)) })
	}
	wg.Wait()
	assert.Assert(t, cmp.Equal(f.sessions(), 1))

	var logged []string
	for _, ev := range cs.get(chatTestRoot).run.view(0).Events {
		assert.Check(t, cmp.Equal(ev.Kind, chunkd.SessionLogMessage))
		logged = append(logged, ev.Text)
	}
	sent := append([]string{f.first(0)}, f.session(0).messages()...)
	slices.Sort(want)
	slices.Sort(logged)
	slices.Sort(sent)
	assert.Check(t, cmp.DeepEqual(logged, want))
	assert.Check(t, cmp.DeepEqual(sent, want))
}

func TestChatsStopAllOnShutdown(t *testing.T) {
	f := newChatFakes()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		rcfg := f.config()
		cs := newChats(ctx, &rcfg)
		a := cs.get("/a")
		mustSend(t, a, chunkd.ChatMessage{Root: "/a", Text: "hi"})
		mustSend(t, cs.get("/b"), chunkd.ChatMessage{Root: "/b", Text: "hi"})
		cancel()
		synctest.Wait()
		assert.Check(t, f.session(0).wasStopped())
		assert.Check(t, f.session(1).wasStopped())
		assert.Check(t, f.record("/a").wasClosed())
		assert.Check(t, f.record("/b").wasClosed())
		assert.Check(t, cs.get("/a") == nil, "a chat was made while shutting down")

		log := mustSend(t, a, chunkd.ChatMessage{Root: "/a", Text: "more"})
		v := a.run.view(log.Next - 1)
		assert.Assert(t, cmp.Len(v.Events, 1))
		assert.Check(t, cmp.DeepEqual(v.Events[0], chunkd.SessionLogEvent{
			Seq: log.Next - 1, Kind: chunkd.SessionLogProblem,
			Text: "the chunk daemon is shutting down; send the message again once it has restarted",
		}))
	})
}

func TestChatRoutes(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	dir := initRepo(t)
	root, err := filepath.EvalSymlinks(dir)
	assert.NilError(t, err)
	f := newChatFakes()
	startTestDaemonWithPlans(t, f.config())

	log, err := chunkd.SendChat(chunkd.ChatMessage{Root: root, Text: "hi"})
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(log.Events, []chunkd.SessionLogEvent{}), "the answer reads from where the log ends")
	assert.Check(t, cmp.Equal(log.Next, 1))
	got, err := chunkd.FetchChat(root, 0)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(got.Events, []chunkd.SessionLogEvent{{Seq: 0, Kind: chunkd.SessionLogMessage, Text: "hi"}}))

	sub := filepath.Join(root, "sub")
	err = os.Mkdir(sub, 0o755)
	assert.NilError(t, err)
	refused := map[string]chunkd.ChatMessage{
		"no text": {
			Root: root,
			Text: "  ",
		},
		"relative root": {
			Root: "repo",
			Text: "hi",
		},
		"root with ..": {
			Root: filepath.Join(sub, "x") + "/../..",
			Text: "hi",
		},
		"not the top level": {
			Root: sub,
			Text: "hi",
		},
		"not a repository": {
			Root: t.TempDir(),
			Text: "hi",
		},
	}
	for name, m := range refused {
		t.Run(name, func(t *testing.T) {
			_, err := chunkd.SendChat(m)
			var sr *chunkd.SessionRefused
			assert.Assert(t, errors.As(err, &sr), "got %v", err)
			assert.Check(t, cmp.Equal(sr.Status, http.StatusBadRequest), sr.Message)
		})
	}

	// Another spelling of the repository reaches the same chat.
	link := filepath.Join(t.TempDir(), "link")
	err = os.Symlink(root, link)
	assert.NilError(t, err)
	_, err = chunkd.SendChat(chunkd.ChatMessage{Root: link, Text: "again"})
	assert.NilError(t, err)
	byLink, err := chunkd.FetchChat(link, 0)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(byLink.Events, 2))
	assert.Check(t, cmp.Equal(byLink.Events[1].Text, "again"))
	assert.Check(t, cmp.Equal(f.sessions(), 1))
	byRoot, err := chunkd.FetchChat(root, 1)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(byRoot.LogID, byLink.LogID))
	assert.Check(t, cmp.Len(byRoot.Events, 1))

	// A repository with no chat has nothing, and asking makes none.
	other, err := filepath.EvalSymlinks(initRepo(t))
	assert.NilError(t, err)
	for range 2 {
		none, err := chunkd.FetchChat(other, 0)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(none, chunkd.SessionLog{Events: []chunkd.SessionLogEvent{}}))
	}

	// With no cursor, only where the log ends.
	end, err := chunkd.FetchChat(root, -1)
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(end.Events, 0))
	assert.Check(t, cmp.Equal(end.Next, 2))

	rec := serve(newTestDaemon(), httptest.NewRequest(http.MethodGet, "/chat?root=/repo&after=x", nil))
	assert.Check(t, cmp.Equal(rec.Code, http.StatusBadRequest))
}

// startTestDaemonWithPlans is startTestDaemon with cfg's seams in place of
// factory and Claude Code.
func startTestDaemonWithPlans(t *testing.T, cfg PlanConfig) {
	t.Helper()
	t.Setenv("CHUNK_DAEMON_DIR", socketDir(t))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- RunDaemon(ctx, nil, "", nil, nil, WithPlans(cfg)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not shut down within 5s")
		}
	})
	sockPath, err := chunkd.SocketPath()
	assert.NilError(t, err)
	waitUntil(t, "daemon to answer", func() bool {
		reachable, _ := ping(sockPath)
		return reachable
	})
}
