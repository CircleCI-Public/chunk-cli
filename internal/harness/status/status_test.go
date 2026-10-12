package status

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/codexauth"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/claude"
	"github.com/smeltdev/smelt/internal/sandboximage"
)

var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func fakeCodex(t *testing.T, body string) codexauth.Host {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	assert.NilError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
	return codexauth.Host{Binary: path}
}

// chatgptLogin is a stored login with an access token that is not read here.
func chatgptLogin() (codexauth.Login, error) {
	return codexauth.Login{Kind: codexauth.KindChatGPT, Auth: []byte(`{"tokens":{"access_token":"a.b.c","refresh_token":"refresh-secret"}}`)}, nil
}

func baseDeps(t *testing.T) Deps {
	t.Helper()
	return Deps{
		Dir: t.TempDir(), Now: func() time.Time { return now },
		LookPath:   func(string) (string, error) { return "/bin/x", nil },
		ClaudeAuth: func(context.Context) (ClaudeLogin, error) { return ClaudeLogin{}, nil },
		CodexLogin: chatgptLogin,
	}
}

func entry(t *testing.T, rep Report, name harness.Name) Entry {
	t.Helper()
	for _, e := range rep.Harnesses {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no %s in report", name)
	return Entry{}
}

func TestCodexReportsTheRecordedAccountAndWindow(t *testing.T) {
	fixture, err := filepath.Abs("../testdata/real/codex-appserver-account.jsonl")
	assert.NilError(t, err)
	d := baseDeps(t)
	d.Codex = fakeCodex(t, "cat "+fixture)
	e := entry(t, Build(t.Context(), d), harness.Codex)
	assert.Equal(t, e.Auth, "ok")
	assert.Equal(t, e.AuthKind, "chatgpt")
	assert.Equal(t, e.Plan, "free")
	assert.Equal(t, len(e.RateLimits), 1)
	assert.Equal(t, e.RateLimits[0].Window, "30d")
	assert.Equal(t, e.RateLimits[0].Source, "app-server")
}

func TestCodexWithoutALoginIsMissing(t *testing.T) {
	d := baseDeps(t)
	d.CodexLogin = func() (codexauth.Login, error) { return codexauth.Login{}, codexauth.ErrNone }
	e := entry(t, Build(t.Context(), d), harness.Codex)
	assert.Equal(t, e.Auth, "missing")
	assert.Equal(t, len(e.RateLimits), 0)
}

func TestCodexAPIKeyIsReportedWithoutAProbe(t *testing.T) {
	d := baseDeps(t)
	d.CodexLogin = func() (codexauth.Login, error) {
		return codexauth.Login{Kind: codexauth.KindAPIKey, APIKey: "sk-secret"}, nil
	}
	e := entry(t, Build(t.Context(), d), harness.Codex)
	assert.Equal(t, e.Auth, "ok")
	assert.Equal(t, e.AuthKind, "api-key")
}

func TestClaudeReportsPlanAndLoginState(t *testing.T) {
	d := baseDeps(t)
	d.ClaudeAuth = func(context.Context) (ClaudeLogin, error) {
		return ClaudeLogin{LoggedIn: true, AuthMethod: "claude.ai", SubscriptionType: "max"}, nil
	}
	e := entry(t, Build(t.Context(), d), harness.Claude)
	assert.Equal(t, e.Auth, "ok")
	assert.Equal(t, e.AuthKind, "subscription")
	assert.Equal(t, e.Plan, "max")

	d.ClaudeAuth = func(context.Context) (ClaudeLogin, error) { return ClaudeLogin{}, nil }
	assert.Equal(t, entry(t, Build(t.Context(), d), harness.Claude).Auth, "missing")
}

func TestClaudeUsageProbeCanBeOffAndMakesNoRequest(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	d := baseDeps(t)
	d.UsageProbe = nil
	Build(t.Context(), d)
	assert.Equal(t, calls.Load(), int32(0))
}

func TestClaudeFallsBackToThePersistedRunSignal(t *testing.T) {
	d := baseDeps(t)
	d.UsageProbe = &claude.UsageProbe{
		CachePath:   filepath.Join(d.Dir, "claude.json"),
		Credentials: func(context.Context) ([]byte, error) { return nil, os.ErrNotExist },
		Now:         d.Now,
	}
	resets := now.Add(2 * time.Hour)
	RecordSignal(d.Dir, harness.Claude, harness.RateLimit{Status: "allowed", Bucket: "5h", ResetsAt: &resets}, now.Add(-time.Minute))

	e := entry(t, Build(t.Context(), d), harness.Claude)
	assert.Equal(t, len(e.RateLimits), 1)
	w := e.RateLimits[0]
	assert.Equal(t, w.Window, "5h")
	assert.Equal(t, w.Source, SourceRun)
	assert.Equal(t, w.Status, "allowed")
	assert.Assert(t, w.ResetsAt.Equal(resets))
	assert.Assert(t, strings.Contains(e.Note, "no Claude Code login"), e.Note)
}

// fakeImages is a Docker host with one image.
type fakeImages struct {
	info    sandboximage.ImageInfo
	probe   func(ctx context.Context) ([]string, error)
	probes  int
	inspect int
}

func (f *fakeImages) InspectImage(context.Context, string) (sandboximage.ImageInfo, error) {
	f.inspect++
	return f.info, nil
}

func (f *fakeImages) RunProbe(ctx context.Context, _ string) ([]string, error) {
	f.probes++
	return f.probe(ctx)
}

func imageDeps(t *testing.T, f *fakeImages) Deps {
	d := baseDeps(t)
	d.ImageTag, d.Images = "smelt-x:abc", f
	return d
}

func TestLabeledImageIsReadFromItsLabelWithoutRunningIt(t *testing.T) {
	f := &fakeImages{info: sandboximage.ImageInfo{Status: sandboximage.ImageLabeled, ID: "sha256:1", Harnesses: []string{"claude"}, Kit: 2}}
	rep := Build(t.Context(), imageDeps(t, f))
	assert.Equal(t, f.probes, 0)
	c, x := entry(t, rep, harness.Claude), entry(t, rep, harness.Codex)
	assert.Assert(t, c.InstalledImage)
	assert.Assert(t, !x.InstalledImage)
	assert.Equal(t, c.ImageStatus, sandboximage.ImageLabeled)
	assert.Equal(t, c.Note, "")
}

func TestUnlabeledImageIsRunOnceAndTheAnswerCached(t *testing.T) {
	f := &fakeImages{
		info:  sandboximage.ImageInfo{Status: sandboximage.ImageUnlabeled, ID: "sha256:1", Kit: 1},
		probe: func(context.Context) ([]string, error) { return []string{"claude"}, nil },
	}
	d := imageDeps(t, f)
	rep := Build(t.Context(), d)
	assert.Equal(t, f.probes, 1)
	c, x := entry(t, rep, harness.Claude), entry(t, rep, harness.Codex)
	assert.Assert(t, c.InstalledImage)
	assert.Assert(t, !x.InstalledImage)
	assert.Equal(t, c.ImageStatus, sandboximage.ImageUnlabeled)
	assert.Assert(t, strings.Contains(c.Note, "image has no smelt.harnesses label (agent kit v1); checked by running it"), c.Note)
	assert.Assert(t, strings.Contains(c.Note, "--update-kit"), c.Note)
	assert.Equal(t, rep.Image.Describe([]string{"claude", "codex"}), "smelt-x:abc (kit v1, unlabeled; claude found, codex not)")

	// The second call finds the answer in the cache.
	rep = Build(t.Context(), d)
	assert.Equal(t, f.probes, 1)
	assert.Assert(t, entry(t, rep, harness.Claude).InstalledImage)

	// A rebuilt image has another ID and is run again.
	f.info.ID = "sha256:2"
	Build(t.Context(), d)
	assert.Equal(t, f.probes, 2)
}

func TestUnlabeledImageProbeThatTimesOutIsNotInstalled(t *testing.T) {
	f := &fakeImages{
		info:  sandboximage.ImageInfo{Status: sandboximage.ImageUnlabeled, ID: "sha256:1", Kit: 1},
		probe: func(ctx context.Context) ([]string, error) { <-ctx.Done(); return nil, ctx.Err() },
	}
	d := imageDeps(t, f)
	d.ProbeTimeout = 20 * time.Millisecond
	rep := Build(t.Context(), d)
	c := entry(t, rep, harness.Claude)
	assert.Assert(t, !c.InstalledImage)
	assert.Assert(t, strings.Contains(c.Note, "failed: timed out"), c.Note)

	// A failed check is not remembered.
	Build(t.Context(), d)
	assert.Equal(t, f.probes, 2)
}

func TestMissingImageIsNoImage(t *testing.T) {
	f := &fakeImages{info: sandboximage.ImageInfo{Status: sandboximage.ImageMissing}}
	rep := Build(t.Context(), imageDeps(t, f))
	c := entry(t, rep, harness.Claude)
	assert.Assert(t, !c.InstalledImage)
	assert.Equal(t, c.ImageStatus, sandboximage.ImageMissing)
	assert.Equal(t, c.Note, "sandbox image smelt-x:abc is not built; run smelt factory image")
	assert.Equal(t, f.probes, 0)
}

func TestReportJSONHasTheDocumentedShapeAndNoSecrets(t *testing.T) {
	d := baseDeps(t)
	d.Default = "claude"
	d.Codex = fakeCodex(t, "exit 0")
	raw, err := json.Marshal(Build(t.Context(), d))
	assert.NilError(t, err)
	var got struct {
		Default   string `json:"default"`
		Harnesses []map[string]any
	}
	assert.NilError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, got.Default, "claude")
	assert.Equal(t, len(got.Harnesses), 2)
	for _, key := range []string{"name", "installed_host", "installed_image", "auth", "rate_limits"} {
		_, ok := got.Harnesses[0][key]
		assert.Assert(t, ok, key)
	}
	assert.Assert(t, !strings.Contains(string(raw), "refresh-secret"))
}
