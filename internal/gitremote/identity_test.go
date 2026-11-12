package gitremote_test

import (
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
)

func TestIdentity(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{
			name: "scp",
			url:  "git@github.com:circleci/factory.git",
			want: "github.com/circleci/factory",
		},
		{
			name: "https without .git",
			url:  "https://github.com/circleci/factory",
			want: "github.com/circleci/factory",
		},
		{
			name: "https with .git",
			url:  "https://github.com/circleci/factory.git",
			want: "github.com/circleci/factory",
		},
		{
			name: "ssh url with port",
			url:  "ssh://git@github.com:22/circleci/factory.git",
			want: "github.com/circleci/factory",
		},
		{
			name: "git url",
			url:  "git://github.com/circleci/factory.git",
			want: "github.com/circleci/factory",
		},
		{
			name: "trailing slash",
			url:  "https://github.com/circleci/factory/",
			want: "github.com/circleci/factory",
		},
		{
			name: "trailing whitespace",
			url:  "git@github.com:circleci/factory.git\n",
			want: "github.com/circleci/factory",
		},
		// A token in the remote must never reach a posted key.
		{
			name: "userinfo with a token",
			url:  "https://user:token@github.com/circleci/factory.git",
			want: "github.com/circleci/factory",
		},
		{
			name: "gitlab subgroup with mixed case",
			url:  "https://GitLab.com/Group/Sub/Repo.git",
			want: "gitlab.com/Group/Sub/Repo",
		},
		{
			name: "scp host case",
			url:  "git@GitHub.com:Org/Repo.git",
			want: "github.com/Org/Repo",
		},
		// A local path names a directory on one machine, no identity another
		// runner could share.
		{
			name:    "absolute path",
			url:     "/srv/git/factory.git",
			wantErr: true,
		},
		{
			name:    "file url",
			url:     "file:///srv/git/factory.git",
			wantErr: true,
		},
		{
			name:    "relative path",
			url:     "../factory",
			wantErr: true,
		},
		{
			name:    "empty",
			url:     "",
			wantErr: true,
		},
		{
			name:    "host only",
			url:     "https://github.com",
			wantErr: true,
		},
		{
			name:    "scp without path",
			url:     "git@github.com:",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitremote.Identity(tt.url)
			if tt.wantErr {
				assert.Assert(t, err != nil, "got %q", got)
				return
			}
			assert.NilError(t, err)
			assert.Check(t, cmp.Equal(got, tt.want))
		})
	}
}
