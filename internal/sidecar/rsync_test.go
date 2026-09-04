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
	cases := []struct {
		name string
		sess *Session
		// want / notWant are matched as substrings of the built command.
		want    []string
		notWant []string
	}{
		{
			name: "agent session pins IdentitiesOnly=no so the agent key can be offered",
			sess: &Session{UseAgent: true, AuthSock: "/tmp/agent.sock"},
			want: []string{
				"ssh -p 2222",
				"-o IdentitiesOnly=no",
				"-o IdentityAgent=/tmp/agent.sock",
			},
			// -q would hide ssh's diagnostics from the rsync error. Discarding the
			// user's config wholesale breaks passphrase-protected keys.
			notWant: []string{"-o IdentitiesOnly=yes", "-F /dev/null", " -q"},
		},
		{
			name: "identity file session restricts ssh to that key",
			sess: &Session{IdentityFile: "/home/dev/.ssh/chunk_ai"},
			want: []string{
				"ssh -p 2222",
				"-o IdentitiesOnly=yes",
				"-i /home/dev/.ssh/chunk_ai",
			},
			// The path is passed raw: rsync splits -e on whitespace and execve's
			// the result, so quoting would embed literal quotes in the filename.
			// IdentityAgent is for the agent path only.
			notWant: []string{"'", " -q", "-F /dev/null", "-o IdentityAgent="},
		},
		{
			name: "identity file takes precedence when an agent is also available",
			sess: &Session{IdentityFile: "/home/dev/.ssh/chunk_ai", UseAgent: true, AuthSock: "/tmp/agent.sock"},
			want: []string{"-o IdentitiesOnly=yes", "-i /home/dev/.ssh/chunk_ai"},
			// Pinning the agent alongside an explicit key would undo IdentitiesOnly.
			notWant: []string{"-o IdentityAgent=", "-o IdentitiesOnly=no"},
		},
		{
			name: "agent session without a socket cannot pin the agent",
			sess: &Session{UseAgent: true},
			// Emitting a bare "-o IdentityAgent=" would point ssh at no agent
			// at all, so neither option is set and ssh falls back to defaults.
			notWant: []string{"-o IdentityAgent=", "-i "},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sshCommand(tc.sess, "2222")
			for _, w := range append(tc.want, hardeningOpts...) {
				assert.Assert(t, strings.Contains(got, w), "want %q in %q", w, got)
			}
			for _, w := range tc.notWant {
				assert.Assert(t, !strings.Contains(got, w), "want %q absent from %q", w, got)
			}
		})
	}
}

// TestSSHCommandOmitsIdentityFileFlag checks that no -i reaches ssh on the
// agent path. Matching the substring "-i " alone would miss an -i emitted as
// the final token, so the built command is tokenised instead.
func TestSSHCommandOmitsIdentityFileFlag(t *testing.T) {
	for _, sess := range []*Session{
		{UseAgent: true, AuthSock: "/tmp/agent.sock"},
		{UseAgent: true},
	} {
		for _, tok := range strings.Fields(sshCommand(sess, "2222")) {
			assert.Assert(t, tok != "-i", "agent path must not pass -i: %q", sshCommand(sess, "2222"))
		}
	}
}

// TestSSHCommandSetsIdentitiesOnlyAtMostOnce guards the exact bug this file's
// helper was extracted for. OpenSSH honours the first occurrence of an option
// and silently ignores the rest, so the original unconditional
// IdentitiesOnly=yes plus a later IdentitiesOnly=no was not a fix.
func TestSSHCommandSetsIdentitiesOnlyAtMostOnce(t *testing.T) {
	for _, sess := range []*Session{
		{UseAgent: true, AuthSock: "/tmp/agent.sock"},
		{IdentityFile: "/home/dev/.ssh/chunk_ai"},
		{IdentityFile: "/home/dev/.ssh/chunk_ai", UseAgent: true, AuthSock: "/tmp/agent.sock"},
		{UseAgent: true},
	} {
		cmd := sshCommand(sess, "2222")
		assert.Assert(t, strings.Count(cmd, "-o IdentitiesOnly=") <= 1,
			"IdentitiesOnly set more than once, later values are ignored: %q", cmd)
	}
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
