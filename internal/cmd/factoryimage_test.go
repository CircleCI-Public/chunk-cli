package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func cfgWith(t *testing.T, body string) *config.ProjectConfig {
	t.Helper()
	var cfg config.ProjectConfig
	assert.NilError(t, json.Unmarshal([]byte(body), &cfg))
	return &cfg
}

func TestResolveBuildSettingsBuildsOnlyDockerfileImages(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	root := t.TempDir()
	streams := discardStreams()

	// A user-managed image passes through untouched, with no Docker involved.
	cfg := cfgWith(t, `{"factory":{"backend":"docker","image":"chunk-sandbox:local","dockerfile":"Dockerfile.chunk"}}`)
	got, err := resolveBuildSettings(context.Background(), streams, root, factorySettings{Attempts: 3}, factoryExplicit{}, cfg)
	assert.NilError(t, err)
	assert.Equal(t, got.Image, "chunk-sandbox:local")
	assert.Equal(t, got.ImageSource, imageFromConfig)

	// A managed one is built, which needs its Dockerfile.
	cfg = cfgWith(t, `{"factory":{"backend":"docker","dockerfile":"Dockerfile.chunk"}}`)
	_, err = resolveBuildSettings(context.Background(), streams, root, factorySettings{Attempts: 3}, factoryExplicit{}, cfg)
	assert.Equal(t, userMsg(err), "Could not read Dockerfile.chunk.")

	// With the Dockerfile present it gets as far as Docker, which is unreachable here.
	assert.NilError(t, os.WriteFile(filepath.Join(root, "Dockerfile.chunk"), []byte("FROM x\n"), 0o644))
	_, err = resolveBuildSettings(context.Background(), streams, root, factorySettings{Attempts: 3}, factoryExplicit{}, cfg)
	assert.Assert(t, err != nil)
}

func TestPrepareDockerfileKeepsAnExistingFile(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	root := t.TempDir()
	path := filepath.Join(root, "Dockerfile.chunk")
	assert.NilError(t, os.WriteFile(path, []byte("FROM mine\n"), 0o644))
	assert.NilError(t, prepareDockerfile(context.Background(), discardStreams(), root, "Dockerfile.chunk", cfgWith(t, `{}`), false))
	got, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Equal(t, string(got), "FROM mine\n")
}

func TestPrepareDockerfileRegenerateNeedsDocker(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	root := t.TempDir()
	path := filepath.Join(root, "Dockerfile.chunk")
	assert.NilError(t, os.WriteFile(path, []byte("FROM mine\n"), 0o644))
	err := prepareDockerfile(context.Background(), discardStreams(), root, "Dockerfile.chunk", cfgWith(t, `{}`), true)
	assert.Assert(t, err != nil)
	got, readErr := os.ReadFile(path)
	assert.NilError(t, readErr)
	assert.Equal(t, string(got), "FROM mine\n") // never overwritten on failure
}
