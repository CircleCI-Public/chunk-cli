package server

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestImageInstallCommandIsDockerOnly(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{{Name: config.CmdInstall, Run: "pnpm install"}}}
	assert.Equal(t, imageInstallCommand(chunkd.BackendDocker, cfg), "pnpm install")
	assert.Equal(t, imageInstallCommand(chunkd.BackendCircleCI, cfg), "")
	assert.Equal(t, imageInstallCommand(chunkd.BackendDocker, &config.ProjectConfig{}), "")
}
