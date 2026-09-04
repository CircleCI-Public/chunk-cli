package sidecar

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestSSHCommand(t *testing.T) {
	cases := []struct {
		name string
		sess *Session
		// want / notWant are matched as substrings of the built command.
		want    []string
		notWant []string
	}{
		{
			name: "agent session omits IdentitiesOnly so the agent key can be offered",
			sess: &Session{UseAgent: true, AuthSock: "/tmp/agent.sock"},
			want: []string{
				"ssh -p 2222",
				"-o StrictHostKeyChecking=no",
				"-o UserKnownHostsFile=/dev/null",
			},
			// -q would hide ssh's diagnostics from the rsync error.
			notWant: []string{"IdentitiesOnly", "-i ", " -q"},
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
			notWant: []string{"'", " -q"},
		},
		{
			name: "identity file takes precedence when an agent is also available",
			sess: &Session{IdentityFile: "/home/dev/.ssh/chunk_ai", UseAgent: true, AuthSock: "/tmp/agent.sock"},
			want: []string{"-o IdentitiesOnly=yes", "-i /home/dev/.ssh/chunk_ai"},
		},
		{
			name:    "empty session behaves like the agent path",
			sess:    &Session{},
			want:    []string{"ssh -p 2222"},
			notWant: []string{"IdentitiesOnly"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sshCommand(tc.sess, "2222")
			for _, w := range tc.want {
				assert.Assert(t, strings.Contains(got, w), "want %q in %q", w, got)
			}
			for _, w := range tc.notWant {
				assert.Assert(t, !strings.Contains(got, w), "want %q absent from %q", w, got)
			}
		})
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
		{},
	} {
		cmd := sshCommand(sess, "2222")
		assert.Assert(t, strings.Count(cmd, "-o IdentitiesOnly=") <= 1,
			"IdentitiesOnly set more than once, later values are ignored: %q", cmd)
	}
}
