package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	appversion "github.com/smeltdev/smelt/internal/version"
)

// pluginDir is a PATH directory holding an executable smelt-<name> script.
func pluginDir(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "smelt-"+name), []byte("#!/bin/sh\n"+script), 0o755))
	return dir
}

func TestPluginName(t *testing.T) {
	for args, want := range map[string]string{"echo a": "echo", "my-tool": "my-tool", "../x": "", "Echo": "", "": "", "-v": "", "a/b": "", "1x": ""} {
		got, ok := pluginName(strings.Fields(args))
		assert.Equal(t, got, want, args)
		assert.Equal(t, ok, want != "", args)
	}
}

func TestFindPluginIgnoresRelativePathEntries(t *testing.T) {
	dir := pluginDir(t, "echo", "")
	t.Chdir(dir)
	assert.Equal(t, findPlugin("echo", "."+string(os.PathListSeparator)+""), "")
	assert.Equal(t, findPlugin("echo", dir), filepath.Join(dir, "smelt-echo"))
	assert.Equal(t, findPlugin("nosuch", dir), "")
}

func TestDispatchPluginRunsItWithArgsEnvAndExitCode(t *testing.T) {
	dir := pluginDir(t, "echo", `echo "args=$*"; echo "bin=$SMELT_BIN"; echo "version=$SMELT_VERSION"; exit 3`)
	old := appversion.Value
	appversion.Value = "1.2.3"
	t.Cleanup(func() { appversion.Value = old })
	self, err := os.Executable()
	assert.NilError(t, err)
	root := NewRootCmd("1.2.3")
	var out bytes.Buffer
	root.SetOut(&out)

	ran, err := dispatchPlugin(root, []string{"echo", "a", "b"}, dir)
	assert.Assert(t, ran)
	var ec interface{ ExitCode() int }
	assert.Assert(t, asExitCode(err, &ec))
	assert.Equal(t, ec.ExitCode(), 3)
	assert.Assert(t, strings.Contains(out.String(), "args=a b"), out.String())
	assert.Assert(t, strings.Contains(out.String(), "\nbin="+self+"\n"), out.String())
	assert.Assert(t, strings.Contains(out.String(), "\nversion=1.2.3\n"), out.String())
}

func asExitCode(err error, target *interface{ ExitCode() int }) bool {
	ec, ok := err.(interface{ ExitCode() int })
	*target = ec
	return ok
}

func TestDispatchPluginNeverRunsABuiltinOrABadName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"factory", "Echo"} {
		assert.NilError(t, os.WriteFile(filepath.Join(dir, "smelt-"+name), []byte("#!/bin/sh\nexit 9\n"), 0o755))
	}
	root := NewRootCmd("dev")
	for _, args := range [][]string{{"factory"}, {"Echo"}, {"../x"}, {}} {
		ran, err := dispatchPlugin(root, args, dir)
		assert.NilError(t, err)
		assert.Assert(t, !ran, args)
	}
}

func TestPluginsHelpListsPluginsButNotBuiltins(t *testing.T) {
	dir := pluginDir(t, "hello", "")
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "smelt-factory"), []byte("#!/bin/sh\n"), 0o755))
	root := NewRootCmd("dev")
	help := pluginsHelp(root, dir)
	assert.Assert(t, strings.Contains(help, "Plugins:\n  smelt hello\n"), help)
	assert.Assert(t, !strings.Contains(help, "factory"), help)
	assert.Equal(t, pluginsHelp(root, t.TempDir()), "")
}

func TestUsageErrors(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct{ args, want string }{
		{"refresh a b c", "accepts at most 1 arg(s), received 3"},
		{"nosuch", "No smelt-nosuch plugin is installed either."},
	} {
		root := NewRootCmd("dev")
		root.SetArgs(strings.Fields(tc.args))
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		old := os.Args
		os.Args = append([]string{"smelt"}, strings.Fields(tc.args)...)
		err := ExecuteRoot(root)
		os.Args = old
		var ue *userError
		assert.Assert(t, asUserError(err, &ue), "%s: %v", tc.args, err)
		assert.Equal(t, ue.UserExitCode(), ExitBadArgs)
		assert.Equal(t, ue.ErrorCode(), "command.invalid_args")
		assert.Assert(t, strings.Contains(ue.UserMessage(), tc.want), ue.UserMessage())
	}
}
