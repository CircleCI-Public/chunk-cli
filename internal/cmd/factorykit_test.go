package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/envspec"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/sandboximage"
)

// kitProject is a project root with a Dockerfile.smelt holding body, and an
// isolated data dir.
func kitProject(t *testing.T, body string) string {
	t.Helper()
	isolateConfig(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(root, "Dockerfile.smelt"), []byte(body), 0o644))
	return root
}

func capturedStreams() (iostream.Streams, *bytes.Buffer) {
	var errOut bytes.Buffer
	return iostream.Streams{Out: &bytes.Buffer{}, Err: &errOut}, &errOut
}

const kitV1Dockerfile = "FROM base\nRUN my-toolchain\n\n# --- smelt agent kit ---\nRUN curl claude\nCMD [\"sleep\", \"infinity\"]\n"

func TestNoteKitOutdatedWarnsOncePerDay(t *testing.T) {
	root := kitProject(t, kitV1Dockerfile)
	streams, out := capturedStreams()
	day := time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local)
	n := kitNotice{Root: root, Dockerfile: "Dockerfile.smelt", Now: day}

	got := noteKitOutdated(streams, n)
	assert.Assert(t, strings.Contains(got, "agent kit v1; the current kit is v3"), got)
	assert.Equal(t, strings.Count(out.String(), "Dockerfile.smelt was generated with agent kit v1"), 1)
	assert.Assert(t, strings.Contains(out.String(), "smelt factory image --update-kit"))

	// The same day it is still reported to the caller, but not printed again.
	out.Reset()
	assert.Assert(t, noteKitOutdated(streams, n) != "")
	assert.Equal(t, out.String(), "")

	// Another day prints it again; so does asking for it every time.
	n.Now = day.AddDate(0, 0, 1)
	noteKitOutdated(streams, n)
	assert.Assert(t, out.Len() > 0)
	out.Reset()
	n.EveryTime = true
	noteKitOutdated(streams, n)
	assert.Assert(t, out.Len() > 0)
}

func TestNoteKitOutdatedSaysNothingForACurrentOrNewerKit(t *testing.T) {
	for _, body := range []string{
		"FROM x\n# --- smelt agent kit (v3) ---\n",
		"FROM x\n# --- smelt agent kit (v9) ---\n",
	} {
		root := kitProject(t, body)
		streams, out := capturedStreams()
		got := noteKitOutdated(streams, kitNotice{Root: root, Dockerfile: "Dockerfile.smelt", Now: time.Now(), EveryTime: true})
		assert.Equal(t, got, "")
		assert.Equal(t, out.String(), "")
	}
}

func TestResolveBuildSettingsWarnsAboutAnOldKitOnceAndKeepsGoing(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	// Docker is unreachable here; a cancelled context ends the wait for it at
	// once, after the warning and before any build.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	root := kitProject(t, kitV1Dockerfile)
	cfg := cfgWith(t, `{"factory":{"backend":"docker","dockerfile":"Dockerfile.smelt"}}`)

	streams, out := capturedStreams()
	got, err := resolveBuildSettings(ctx, streams, root, factorySettings{Attempts: 3}, factoryExplicit{}, cfg)
	assert.Assert(t, err != nil, "Docker is unreachable here, so the build is where it stops")
	assert.Assert(t, got.KitWarning != "")
	assert.Equal(t, strings.Count(out.String(), "smelt factory image --update-kit"), 1)

	out.Reset()
	got, _ = resolveBuildSettings(ctx, streams, root, factorySettings{Attempts: 3}, factoryExplicit{}, cfg)
	assert.Assert(t, got.KitWarning != "", "the run still records it")
	assert.Assert(t, !strings.Contains(out.String(), "update-kit"), out.String())
}

func TestUpdateKitChangesOnlyTheKitSection(t *testing.T) {
	root := kitProject(t, kitV1Dockerfile)
	streams, out := capturedStreams()
	assert.NilError(t, updateKit(t.Context(), streams, kitUpdate{Root: root, Dockerfile: "Dockerfile.smelt", Yes: true}))

	got, err := os.ReadFile(filepath.Join(root, "Dockerfile.smelt"))
	assert.NilError(t, err)
	head, _, _ := strings.Cut(kitV1Dockerfile, "# --- smelt agent kit")
	assert.Assert(t, strings.HasPrefix(string(got), head+"# --- smelt agent kit (v3) "), string(got))
	assert.Assert(t, strings.Contains(string(got), sandboximage.KitCodex()))
	assert.Assert(t, strings.Contains(string(got), "smelt.kit=3"))
	assert.Assert(t, strings.Contains(out.String(), "+LABEL smelt.harnesses"), out.String())
	assert.Equal(t, kitWarning(root, "Dockerfile.smelt"), "")

	// Updating again changes nothing.
	assert.NilError(t, updateKit(t.Context(), streams, kitUpdate{Root: root, Dockerfile: "Dockerfile.smelt", Yes: true}))
	again, _ := os.ReadFile(filepath.Join(root, "Dockerfile.smelt"))
	assert.Equal(t, string(again), string(got))
}

func TestUpdateKitMatchesARenderedDockerfile(t *testing.T) {
	rendered := sandboximage.Render(sandboximage.NewRecipe(&envspec.Environment{Stack: "unknown"}, nil, nil))
	head, _, _ := strings.Cut(rendered, "# --- smelt agent kit")
	root := kitProject(t, head+"# --- smelt agent kit ---\nRUN old\n")
	streams, _ := capturedStreams()
	assert.NilError(t, updateKit(t.Context(), streams, kitUpdate{Root: root, Dockerfile: "Dockerfile.smelt", Yes: true}))
	got, _ := os.ReadFile(filepath.Join(root, "Dockerfile.smelt"))
	assert.Equal(t, string(got), rendered)
}

func TestUpdateKitWithoutAMarkerIsRefused(t *testing.T) {
	root := kitProject(t, "FROM x\nRUN true\n")
	streams, _ := capturedStreams()
	err := updateKit(t.Context(), streams, kitUpdate{Root: root, Dockerfile: "Dockerfile.smelt", Yes: true})
	assert.Equal(t, userMsg(err), "Dockerfile.smelt has no smelt agent kit section, so its kit cannot be updated in place.")
	var ue *userError
	assert.Assert(t, asUserError(err, &ue))
	assert.Assert(t, strings.Contains(ue.suggestion, "--regenerate"), ue.suggestion)
	got, _ := os.ReadFile(filepath.Join(root, "Dockerfile.smelt"))
	assert.Equal(t, string(got), "FROM x\nRUN true\n")
}

func TestUpdateKitNeedsConfirmationWithoutATerminal(t *testing.T) {
	root := kitProject(t, kitV1Dockerfile)
	streams, _ := capturedStreams()
	err := updateKit(t.Context(), streams, kitUpdate{Root: root, Dockerfile: "Dockerfile.smelt"})
	assert.Equal(t, userMsg(err), "Updating the agent kit needs confirmation.")
	got, _ := os.ReadFile(filepath.Join(root, "Dockerfile.smelt"))
	assert.Equal(t, string(got), kitV1Dockerfile)
}

func TestFactoryImageRejectsRegenerateWithUpdateKit(t *testing.T) {
	_, _, err := runCobra(t, newFactoryImageCmd(), "--regenerate", "--update-kit")
	assert.Equal(t, userMsg(err), "--regenerate and --update-kit cannot be used together.")
}
