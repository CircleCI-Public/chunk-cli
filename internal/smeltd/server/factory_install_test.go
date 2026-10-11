package server

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/smeltd"
)

func TestImageInstallCommandIsDockerOnly(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{{Name: config.CmdInstall, Run: "pnpm install"}}}
	assert.Equal(t, imageInstallCommand(smeltd.BackendDocker, cfg), "pnpm install")
	assert.Equal(t, imageInstallCommand(smeltd.BackendCircleCI, cfg), "")
	assert.Equal(t, imageInstallCommand(smeltd.BackendDocker, &config.ProjectConfig{}), "")
}
