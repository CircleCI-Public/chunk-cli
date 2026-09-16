package sidecar

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// hardeningOpts are pinned on every session type. A user ssh_config cannot
// override them (command-line options are parsed first), and the options it
// could otherwise interfere through are neutralised one by one so the rest of
// the user's config — /etc/ssh/ssh_config, UseKeychain — keeps working.
var hardeningOpts = []string{
	"-o StrictHostKeyChecking=no",
	"-o UserKnownHostsFile=/dev/null",
	"-o ProxyCommand=none",
	"-o ProxyJump=none",
	"-o ControlPath=none",
	"-o RequestTTY=no",
}

func TestSSHCommand(t *testing.T) {
	sess := &Session{IdentityFile: "/home/dev/.ssh/chunk_ai"}
	got := sshCommand(sess, "2222")

	want := append([]string{
		"ssh -p 2222",
		"-o IdentitiesOnly=yes",
		"-i /home/dev/.ssh/chunk_ai",
	}, hardeningOpts...)
	for _, w := range want {
		assert.Assert(t, strings.Contains(got, w), "want %q in %q", w, got)
	}

	// The key path is passed raw: rsync splits -e on whitespace and execve's the
	// result, so quoting would embed literal quotes in the filename. -q would
	// hide ssh's diagnostics from the rsync error, and discarding the user's
	// config wholesale breaks passphrase-protected keys.
	for _, w := range []string{"'", " -q", "-F /dev/null"} {
		assert.Assert(t, !strings.Contains(got, w), "want %q absent from %q", w, got)
	}
}

// TestSSHCommandSetsIdentitiesOnlyAtMostOnce guards the reason this helper was
// extracted. OpenSSH honours the first occurrence of an option and silently
// ignores the rest, so a repeated IdentitiesOnly would make the later value dead
// weight rather than an override.
func TestSSHCommandSetsIdentitiesOnlyAtMostOnce(t *testing.T) {
	cmd := sshCommand(&Session{IdentityFile: "/home/dev/.ssh/chunk_ai"}, "2222")
	assert.Assert(t, strings.Count(cmd, "-o IdentitiesOnly=") <= 1,
		"IdentitiesOnly set more than once, later values are ignored: %q", cmd)
}

func TestRsyncErrDetail(t *testing.T) {
	const hostKeyNotice = "Warning: Permanently added '[127.0.0.1]:52134' (ED25519) to the list of known hosts."

	cases := []struct {
		name   string
		stderr string
		want   string
	}{
		{
			name:   "drops the expected host key notice so the real cause leads",
			stderr: hostKeyNotice + "\nrsync: mkpath: Read-only file system\nrsync: error: unexpected end of file",
			want:   "rsync: mkpath: Read-only file system\nrsync: error: unexpected end of file",
		},
		{
			name:   "notice alone leaves nothing to report",
			stderr: hostKeyNotice + "\n",
			want:   "",
		},
		{
			name:   "genuine ssh diagnostics survive",
			stderr: hostKeyNotice + "\ndev@127.0.0.1: Permission denied (publickey).",
			want:   "dev@127.0.0.1: Permission denied (publickey).",
		},
		{
			name:   "unrelated warnings are kept",
			stderr: "Warning: something else entirely",
			want:   "Warning: something else entirely",
		},
		{
			name:   "empty stderr stays empty",
			stderr: "",
			want:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, rsyncErrDetail(tc.stderr), tc.want)
		})
	}
}
