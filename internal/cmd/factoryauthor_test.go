package cmd

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
)

func TestDecideAuthorChange(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		unset   bool
		want    authorChange
		wantErr string
	}{
		{name: "show", want: authorChange{}},
		{name: "set", args: []string{"Ada Lovelace <ada@example.com>"}, want: authorChange{Write: true, Value: "Ada Lovelace <ada@example.com>"}},
		{name: "set canonicalises", args: []string{`"Ada"   <ada@example.com>`}, want: authorChange{Write: true, Value: "Ada <ada@example.com>"}},
		{name: "unset", unset: true, want: authorChange{Write: true}},
		{name: "unset with a value", args: []string{"Ada <ada@example.com>"}, unset: true, wantErr: "not both"},
		{name: "no name", args: []string{"ada@example.com"}, wantErr: "no name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decideAuthorChange(tt.args, tt.unset)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, got, tt.want)
		})
	}
}

func runFactoryAuthorCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newFactoryAuthorCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	cmd.SetContext(t.Context())
	err := cmd.Execute()
	return out.String(), err
}

func TestFactoryAuthorCommand(t *testing.T) {
	isolateConfig(t)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := initGitRepo(t)
	t.Chdir(dir)
	for _, kv := range [][2]string{{"user.name", "Dev Eloper"}, {"user.email", "dev@example.com"}} {
		assert.NilError(t, exec.Command("git", "-C", dir, "config", kv[0], kv[1]).Run())
	}

	// Showing works before smelt init; setting needs a config to write to.
	out, err := runFactoryAuthorCmd(t)
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(out), "Factory commits as: Dev Eloper <dev@example.com> (your git identity)")
	_, err = runFactoryAuthorCmd(t, "Ada <ada@example.com>")
	assert.Assert(t, strings.Contains(userMsg(err), "no .smelt/config.json"))

	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{Factory: &config.FactoryConfig{Backend: "docker"}}))
	out, err = runFactoryAuthorCmd(t, "Ada <ada@example.com>")
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(out), "Factory commits as: Ada <ada@example.com> (factory.author in .smelt/config.json)")
	cfg, err := config.LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, cfg.Factory, &config.FactoryConfig{Backend: "docker", Author: "Ada <ada@example.com>"})

	out, err = runFactoryAuthorCmd(t, "--unset")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "(your git identity)"), out)
	cfg, err = config.LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Equal(t, cfg.Factory.Author, "")
}
