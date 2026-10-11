package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newFactoryAuthorCmd() *cobra.Command {
	var unset bool
	cmd := &cobra.Command{
		Use:   `author ["Name <email>"]`,
		Short: "Show or set who factory commits are attributed to",
		Long: `Show or set who the commits on a factory run's branch are attributed to.
Those commits are the ones a published PR carries.

With no argument, print the author runs in this repository use and where it
comes from: factory.author in .chunk/config.json, else your git user.name and
user.email, else chunk.

With an argument, save it as factory.author. .chunk/config.json is committed,
so everyone who runs the factory in this repository then commits as that
author. --unset removes it, so each person commits as themselves again.`,
		Example: `  chunk factory author
  chunk factory author "Ada Lovelace <ada@example.com>"
  chunk factory author --unset`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFactoryAuthor(cmd.Context(), iostream.FromCmd(cmd), args, unset)
		},
	}
	cmd.Flags().BoolVar(&unset, "unset", false, "remove factory.author, so each person commits as themselves")
	return cmd
}

// authorChange is what `chunk factory author` was asked to do to factory.author.
type authorChange struct {
	// Write is false when the command only shows the author.
	Write bool
	// Value is the new factory.author, "" to remove it.
	Value string
}

// decideAuthorChange reads the command's arguments. The value is stored in
// its canonical "Name <email>" form.
func decideAuthorChange(args []string, unset bool) (authorChange, error) {
	switch {
	case unset && len(args) > 0:
		return authorChange{}, errors.New("pass an author or --unset, not both")
	case unset:
		return authorChange{Write: true}, nil
	case len(args) == 0:
		return authorChange{}, nil
	}
	author, err := config.ParseCommitAuthor(args[0])
	if err != nil {
		return authorChange{}, err
	}
	return authorChange{Write: true, Value: author.String()}, nil
}

func runFactoryAuthor(ctx context.Context, streams iostream.Streams, args []string, unset bool) error {
	change, err := decideAuthorChange(args, unset)
	if err != nil {
		return newUserError(fmt.Sprintf("Invalid author: %v.", err)).
			withCode("command.invalid_args").
			withSuggestion(`Write it as "Name <email>", in quotes.`).
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	dir, err := os.Getwd()
	if err != nil {
		return &userError{msg: msgCouldNotDetermineWorkDir, err: err}
	}
	root := gitutil.TopLevelCtx(ctx, dir)
	if root == "" {
		return newUserError(fmt.Sprintf("%s is not inside a git repository.", dir)).
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}

	cfg, err := config.LoadProjectConfig(root)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist) && !change.Write:
		cfg = &config.ProjectConfig{}
	case errors.Is(err, os.ErrNotExist):
		return newUserError("This repository has no .chunk/config.json yet.").
			withSuggestion("Run chunk init first.").
			withExitCode(ExitBadArgs).
			withoutDetail()
	default:
		return &userError{msg: "Could not read .chunk/config.json.", err: err}
	}

	if change.Write {
		if cfg.Factory == nil {
			cfg.Factory = &config.FactoryConfig{}
		}
		cfg.Factory.Author = change.Value
		if err := writeProjectConfig(root, cfg, streams); err != nil {
			return err
		}
	}
	// The config was validated on load or built from a parsed value.
	configured, _ := cfg.Factory.CommitAuthor()
	streams.Println(describeAuthor(factory.ResolveAuthor(ctx, root, configured)))
	if change.Value != "" {
		streams.ErrPrintln(ui.Dim("factory.author is in .chunk/config.json, which is committed: everyone who runs the factory here commits as this author."))
	}
	return nil
}

// describeAuthor says who factory commits are attributed to and why.
func describeAuthor(r factory.ResolvedAuthor) string {
	var source string
	switch r.Source {
	case factory.AuthorFromConfig:
		source = "factory.author in .chunk/config.json"
	case factory.AuthorFromGit:
		source = "your git identity"
	case factory.AuthorDefault:
		source = "no factory.author or git identity is set"
	}
	return fmt.Sprintf("Factory commits as: %s (%s)", r.CommitAuthor, source)
}

// askAuthor asks who factory commits are attributed to. developer is the
// author runs would use with no factory.author. It returns the factory.author
// to save, "" to keep using the developer's own identity; ok is false when the
// user cancelled or the prompt could not be shown.
func askAuthor(streams iostream.Streams, developer factory.ResolvedAuthor) (value string, ok bool) {
	current := "chunk (no git identity is set)"
	if developer.Source == factory.AuthorFromGit {
		current = "Me, from git: " + developer.String()
	}
	idx, err := ui.SelectFromList("Who should factory commits be attributed to?", []string{current, "Someone else"})
	if err != nil {
		warnPromptError(streams, err)
		return "", false
	}
	if idx == 0 {
		return "", true
	}
	answer, err := ui.PromptText("Commit as (Name <email>)", "")
	if err != nil {
		warnPromptError(streams, err)
		return "", false
	}
	author, err := config.ParseCommitAuthor(answer)
	if err != nil {
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Not saving the author: %v. Set it later with chunk factory author.", err)))
		return "", false
	}
	return author.String(), true
}
