package factory

import (
	"context"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
)

// The identity chunk commits as when nobody else is named, so a run never
// stops to ask for one.
const (
	defaultAuthorName  = "chunk"
	defaultAuthorEmail = "chunk@circleci.com"
)

// AuthorSource is where a resolved author came from.
type AuthorSource int

// The places an author can come from, in the order ResolveAuthor tries them.
const (
	AuthorFromConfig AuthorSource = iota // factory.author
	AuthorFromGit                        // the developer's git user.name and user.email
	AuthorDefault                        // nobody was named, so chunk
)

// ResolvedAuthor is the author of a run's commits and where it came from.
type ResolvedAuthor struct {
	config.CommitAuthor
	Source AuthorSource
}

// ResolveAuthor picks who the commits on a run's branch are attributed to: the
// configured author (factory.author), else the developer's git identity in
// root, else chunk. Those commits are the ones a published PR carries.
func ResolveAuthor(ctx context.Context, root string, configured config.CommitAuthor) ResolvedAuthor {
	return chooseAuthor(configured, developerIdentity(ctx, root))
}

// chooseAuthor returns the first complete identity, falling back to chunk's.
func chooseAuthor(configured, developer config.CommitAuthor) ResolvedAuthor {
	switch {
	case configured.Complete():
		return ResolvedAuthor{CommitAuthor: configured, Source: AuthorFromConfig}
	case developer.Complete():
		return ResolvedAuthor{CommitAuthor: developer, Source: AuthorFromGit}
	}
	return ResolvedAuthor{
		CommitAuthor: config.CommitAuthor{Name: defaultAuthorName, Email: defaultAuthorEmail},
		Source:       AuthorDefault,
	}
}

// developerIdentity is git's user.name and user.email in root, empty when
// either is unset.
func developerIdentity(ctx context.Context, root string) config.CommitAuthor {
	git := gitexec.Runner{Dir: root}
	name, err := git.Output(ctx, "config", "user.name")
	if err != nil {
		return config.CommitAuthor{}
	}
	email, err := git.Output(ctx, "config", "user.email")
	if err != nil {
		return config.CommitAuthor{}
	}
	return config.CommitAuthor{Name: strings.TrimSpace(string(name)), Email: strings.TrimSpace(string(email))}
}

// commitEnv commits as author, or as chunk when author is incomplete, whatever
// the repository's own identity settings say.
func commitEnv(author config.CommitAuthor) []string {
	a := chooseAuthor(author, config.CommitAuthor{})
	return append(os.Environ(),
		"GIT_AUTHOR_NAME="+a.Name, "GIT_AUTHOR_EMAIL="+a.Email,
		"GIT_COMMITTER_NAME="+a.Name, "GIT_COMMITTER_EMAIL="+a.Email)
}
