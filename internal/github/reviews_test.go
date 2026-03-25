package github

import "testing"

func TestIsBot(t *testing.T) {
	tests := []struct {
		login string
		want  bool
	}{
		{"dependabot[bot]", true},
		{"renovate-bot", true},
		{"circleci-app", true},
		{"wiz-inc-scanner", true},
		{"github-actions", true},
		{"dependabot", true},
		{"renovate", true},
		{"codecov", true},
		{"sonarcloud", true},
		{"alice", false},
		{"bob-builder", false},
		{"my-bot-account", false}, // no trailing -bot
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.login, func(t *testing.T) {
			if got := isBot(tt.login); got != tt.want {
				t.Fatalf("isBot(%q) = %v, want %v", tt.login, got, tt.want)
			}
		})
	}
}
