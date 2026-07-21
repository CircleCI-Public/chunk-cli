package telemetry

import "testing"

func TestDetectCodingAgent(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "no signals", env: nil, want: ""},
		{name: "claude code", env: map[string]string{envClaudeCode: "1"}, want: agentClaudeCode},
		{name: "cursor", env: map[string]string{envCursor: "abc123"}, want: agentCursor},
		{
			name: "first match wins",
			env: map[string]string{
				envClaudeCode: "1",
				envCursor:     "abc123",
			},
			want: agentClaudeCode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, sig := range agentEnvSignals() {
				t.Setenv(sig.env, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			if got := DetectCodingAgent(); got != tt.want {
				t.Errorf("DetectCodingAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}
