package codexauth

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/version"
)

// probeTimeout bounds the whole conversation with codex app-server.
const probeTimeout = 30 * time.Second

// The values of Account.Auth.
const (
	AuthOK      = "ok"
	AuthMissing = "missing"
	AuthError   = "error"
)

// SourceAppServer labels figures read from codex app-server.
const SourceAppServer = "app-server"

// Account is what codex app-server says about a login.
type Account struct {
	// Auth is AuthOK, AuthMissing or AuthError.
	Auth string
	// Plan is the ChatGPT plan, such as "plus" or "free".
	Plan    string
	Windows []harness.Window
	// Err explains AuthError.
	Err string
}

// Probe asks codex app-server for the account and its rate limits. auth is the
// stored auth.json; its refresh token is blanked before it is written, which is
// enough for these calls. An empty auth is a missing login.
func (h Host) Probe(ctx context.Context, auth []byte, now time.Time) Account {
	if len(auth) == 0 {
		return Account{Auth: AuthMissing}
	}
	cp, err := ContainerCopy(auth)
	if err != nil {
		return Account{Auth: AuthError, Err: err.Error()}
	}
	home, err := os.MkdirTemp("", "smelt-codex-status-")
	if err != nil {
		return Account{Auth: AuthError, Err: err.Error()}
	}
	defer func() { _ = os.RemoveAll(home) }()
	if err := os.WriteFile(filepath.Join(home, authFile), cp, 0o600); err != nil {
		return Account{Auth: AuthError, Err: err.Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.binary(), "app-server")
	cmd.Env = withCodexHome(home)
	in, err := cmd.StdinPipe()
	if err != nil {
		return Account{Auth: AuthError, Err: err.Error()}
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return Account{Auth: AuthError, Err: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return Account{Auth: AuthError, Err: err.Error()}
	}
	defer func() { _ = cmd.Wait() }()
	defer cancel()
	return Converse(in, out, now)
}

// Request ids of the conversation.
const (
	idInit = iota + 1
	idAccount
	idLimits
)

// Converse speaks the app-server protocol over a pipe pair: one JSON object per
// line, no jsonrpc field. It sends initialize, the initialized notification and
// the two reads, and keeps in open until the replies arrive.
func Converse(in io.WriteCloser, out io.Reader, now time.Time) Account {
	defer func() { _ = in.Close() }()
	send := func(r request) {
		b, _ := json.Marshal(r)
		// A server that has already answered and exited is fine; what is
		// missing from its answers is reported below.
		_, _ = in.Write(append(b, '\n'))
	}
	type obj = map[string]any
	send(request{ID: idInit, Method: "initialize", Params: obj{"clientInfo": obj{"name": "smelt", "version": version.Value}}})
	send(request{Method: "initialized"})
	send(request{ID: idAccount, Method: "account/read", Params: obj{}})
	send(request{ID: idLimits, Method: "account/rateLimits/read"})

	replies := readReplies(out, idAccount, idLimits)
	return account(replies[idAccount], replies[idLimits], now)
}

// request is one line sent to the server; a notification has no id.
type request struct {
	ID     int    `json:"id,omitempty"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type reply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
	ID *int `json:"id"`
}

// readReplies collects replies by id until every wanted one is in or out ends.
func readReplies(out io.Reader, want ...int) map[int]reply {
	got := map[int]reply{}
	sc := bufio.NewScanner(out)
	sc.Buffer(nil, 4<<20)
	for len(got) < len(want) && sc.Scan() {
		var r reply
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.ID == nil {
			continue
		}
		for _, id := range want {
			if id == *r.ID {
				got[id] = r
			}
		}
	}
	return got
}

// account turns the two replies into an Account.
func account(acct, limits reply, now time.Time) Account {
	switch {
	case acct.Error != nil && authRequired(acct.Error.Message):
		return Account{Auth: AuthMissing}
	case acct.Error != nil:
		return Account{Auth: AuthError, Err: acct.Error.Message}
	case acct.Result == nil:
		return Account{Auth: AuthError, Err: "codex app-server gave no account"}
	}
	var a struct {
		Account *struct {
			PlanType string `json:"planType"`
		} `json:"account"`
	}
	if err := json.Unmarshal(acct.Result, &a); err != nil || a.Account == nil {
		return Account{Auth: AuthMissing}
	}
	res := Account{Auth: AuthOK, Plan: a.Account.PlanType}
	if limits.Result != nil {
		res.Windows = windows(limits.Result, now)
	}
	return res
}

func authRequired(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "authentication required")
}

type rateWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

// windows maps rateLimits.primary and .secondary to windows, named by length.
func windows(result json.RawMessage, now time.Time) []harness.Window {
	var r struct {
		RateLimits struct {
			Primary   *rateWindow `json:"primary"`
			Secondary *rateWindow `json:"secondary"`
		} `json:"rateLimits"`
	}
	if json.Unmarshal(result, &r) != nil {
		return nil
	}
	var out []harness.Window
	for _, w := range []*rateWindow{r.RateLimits.Primary, r.RateLimits.Secondary} {
		if w == nil {
			continue
		}
		used := w.UsedPercent
		win := harness.Window{
			Window: harness.WindowName(w.WindowDurationMins), UsedPercent: &used,
			Source: SourceAppServer, SeenAt: &now,
		}
		if w.ResetsAt > 0 {
			t := time.Unix(w.ResetsAt, 0).UTC()
			win.ResetsAt = &t
		}
		out = append(out, win)
	}
	return out
}
