// Package status answers `smelt harness status`: per harness, what is
// installed, whether its login works, and the best usage data to hand. It
// keeps one piece of state, the last rate-limit signal a run saw; the choice of
// harness from this data is the caller's.
package status

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/smeltdev/smelt/internal/codexauth"
	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/claude"
)

// The values of Entry.Auth beyond codexauth's.
const (
	// SourceRun labels figures taken from a run's rate-limit signal.
	SourceRun = "run"
	// AuthKindSubscription is a Claude login that bills against a plan.
	AuthKindSubscription = "subscription"
)

// Report is the answer of `smelt harness status`.
type Report struct {
	// Default is factory.harness from the project config, if any.
	Default   string  `json:"default"`
	Harnesses []Entry `json:"harnesses"`
}

// Entry is one harness.
type Entry struct {
	Name           harness.Name     `json:"name"`
	InstalledHost  bool             `json:"installed_host"`
	InstalledImage bool             `json:"installed_image"`
	Auth           string           `json:"auth"`
	AuthKind       string           `json:"auth_kind,omitempty"`
	Plan           string           `json:"plan,omitempty"`
	RateLimits     []harness.Window `json:"rate_limits"`
	// Note says why something above is missing or stale.
	Note string `json:"note,omitempty"`
}

// Deps are the outside world Build looks at. Every field has a working
// default, so tests replace only what they fake.
type Deps struct {
	// Default is factory.harness.
	Default string
	// ImageHarnesses are the agents the sandbox image carries, from its label.
	ImageHarnesses []string
	// Dir holds the last rate-limit signal of each harness.
	Dir string
	// UsageProbe is the Claude probe; nil turns it off.
	UsageProbe *claude.UsageProbe
	Codex      codexauth.Host
	// LookPath finds a binary; exec.LookPath when nil.
	LookPath func(string) (string, error)
	// ClaudeAuth reads `claude auth status --json`; the real CLI when nil.
	ClaudeAuth func(ctx context.Context) (ClaudeLogin, error)
	// CodexLogin finds the stored Codex credential; codexauth.Find when nil.
	CodexLogin func() (codexauth.Login, error)
	Now        func() time.Time
}

// ClaudeLogin is what `claude auth status --json` says.
type ClaudeLogin struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	SubscriptionType string `json:"subscriptionType"`
}

// Build reports on every harness, Claude first. The harnesses are probed
// concurrently: each probe is a subprocess or a request.
func Build(ctx context.Context, d Deps) Report {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	rep := Report{Default: d.Default, Harnesses: make([]Entry, 2)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		rep.Harnesses[0] = d.claude(ctx)
	}()
	rep.Harnesses[1] = d.codex(ctx)
	<-done
	return rep
}

func (d Deps) base(name harness.Name, binary string) Entry {
	_, err := d.LookPath(binary)
	return Entry{
		Name: name, InstalledHost: err == nil,
		InstalledImage: slices.Contains(d.ImageHarnesses, string(name)),
		RateLimits:     []harness.Window{},
	}
}

func (d Deps) claude(ctx context.Context) Entry {
	e := d.base(harness.Claude, claude.Harness{}.Binary())
	login, err := d.claudeLogin(ctx, e.InstalledHost)
	switch {
	case err != nil:
		e.Auth, e.Note = codexauth.AuthError, err.Error()
	case login.LoggedIn:
		e.Auth, e.Plan = codexauth.AuthOK, login.SubscriptionType
		e.AuthKind = login.AuthMethod
		if login.SubscriptionType != "" {
			e.AuthKind = AuthKindSubscription
		}
	default:
		e.Auth = codexauth.AuthMissing
	}
	if d.UsageProbe != nil {
		u := d.UsageProbe.Read(ctx)
		e.RateLimits, e.Note = append(e.RateLimits, u.Windows...), joinNotes(e.Note, u.Note)
	}
	e.RateLimits = d.withRunSignal(e.RateLimits, harness.Claude)
	return e
}

func (d Deps) claudeLogin(ctx context.Context, installed bool) (ClaudeLogin, error) {
	switch {
	case d.ClaudeAuth != nil:
		return d.ClaudeAuth(ctx)
	case !installed:
		return ClaudeLogin{}, nil
	}
	out, err := exec.CommandContext(ctx, claude.Harness{}.Binary(), "auth", "status", "--json").Output()
	if err != nil {
		return ClaudeLogin{}, nil // a logged-out claude exits non-zero
	}
	var l ClaudeLogin
	if err := json.Unmarshal(out, &l); err != nil {
		return ClaudeLogin{}, err
	}
	return l, nil
}

func (d Deps) codex(ctx context.Context) Entry {
	e := d.base(harness.Codex, codexauth.Binary)
	find := d.CodexLogin
	if find == nil {
		find = codexauth.Find
	}
	login, err := find()
	switch {
	case err != nil:
		e.Auth = codexauth.AuthMissing
	case login.Kind == codexauth.KindAPIKey:
		e.Auth, e.AuthKind = codexauth.AuthOK, string(codexauth.KindAPIKey)
	case !e.InstalledHost:
		e.Auth, e.AuthKind = codexauth.AuthOK, string(codexauth.KindChatGPT)
		e.Note = "codex is not installed here, so its login was not checked"
	default:
		acct := d.Codex.Probe(ctx, login.Auth, d.Now())
		e.Auth, e.AuthKind, e.Plan, e.Note = acct.Auth, string(codexauth.KindChatGPT), acct.Plan, acct.Err
		e.RateLimits = append(e.RateLimits, acct.Windows...)
	}
	e.RateLimits = d.withRunSignal(e.RateLimits, harness.Codex)
	return e
}

// withRunSignal adds the last rate-limit signal a run saw, unless a probe
// already reported that window.
func (d Deps) withRunSignal(windows []harness.Window, name harness.Name) []harness.Window {
	seen, ok := LastSignal(d.Dir, name)
	if !ok {
		return windows
	}
	w := harness.Window{Window: seen.Bucket, ResetsAt: seen.ResetsAt, Status: seen.Status, Source: SourceRun, SeenAt: &seen.SeenAt}
	if seen.Utilization != nil {
		pct := float64(*seen.Utilization)
		w.UsedPercent = &pct
	}
	if slices.ContainsFunc(windows, func(o harness.Window) bool { return o.Window == w.Window }) {
		return windows
	}
	return append(windows, w)
}

func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

// Signal is a rate-limit signal a run saw, and when.
type Signal struct {
	harness.RateLimit
	SeenAt time.Time `json:"seen_at"`
}

func signalPath(dir string, name harness.Name) string {
	return filepath.Join(dir, string(name)+".run.json")
}

// RecordSignal keeps rl as the last rate-limit signal of name. It is best
// effort: a run is not worth failing over a status file.
func RecordSignal(dir string, name harness.Name, rl harness.RateLimit, seen time.Time) {
	raw, err := json.Marshal(Signal{RateLimit: rl, SeenAt: seen})
	if err != nil || os.MkdirAll(dir, 0o700) != nil {
		return
	}
	_ = os.WriteFile(signalPath(dir, name), raw, 0o600)
}

// LastSignal is the last rate-limit signal a run of name saw.
func LastSignal(dir string, name harness.Name) (Signal, bool) {
	raw, err := os.ReadFile(signalPath(dir, name))
	if err != nil {
		return Signal{}, false
	}
	var s Signal
	return s, json.Unmarshal(raw, &s) == nil
}

// Dir is where smelt keeps what it knows about the harnesses between runs.
func Dir() (string, error) {
	data, err := config.AppData()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "harness"), nil
}
