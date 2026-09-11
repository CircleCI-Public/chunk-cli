package cmd

import (
	"encoding/json"
	"errors"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// hookOutput is the JSON Claude Code reads from a hook's stdout.
//
// additionalContext is the channel for telling an agent something without
// standing in its way — unlike a non-zero exit, which is how `chunk validate`
// makes a failed check block a commit. Using it here is what makes this
// advisory in fact and not merely in wording.
type hookOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

type hookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

func newConflictsCmd() *cobra.Command {
	var (
		projectDir string
		hookMode   bool
		jsonOut    bool
	)
	cmd := &cobra.Command{
		Use:   "conflicts",
		Short: "Report whether this branch still merges cleanly into its merge target",
		Long: `Report whether this branch still merges cleanly into its merge target.

The answer comes from the watch daemon, which previews the merge in the
background against the default branch it last fetched. Nothing is computed here,
so this returns immediately and never touches the working tree.

The comparison covers committed history only: uncommitted changes are not
considered. A branch with no daemon, no recorded default branch, or a detached
HEAD has no answer, which is reported rather than passed off as "no conflicts".

This command always exits 0. It is advisory, and is not a check that can fail.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConflicts(cmd, projectDir, hookMode, jsonOut)
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "Override project directory")
	cmd.Flags().BoolVar(&hookMode, "hook", false,
		"Emit Claude Code hook JSON, so the notice reaches the agent as advisory context")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the raw conflict report as JSON")
	return cmd
}

// runConflicts never returns an error the user sees as a failure.
//
// Every path returns nil. This is the one guarantee the feature rests on: it
// runs from a hook alongside `chunk validate`, whose non-zero exit is what
// blocks a commit, and an advisory that can exit non-zero is a gate waiting for
// the wrong branch to be taken.
func runConflicts(cmd *cobra.Command, projectDir string, hookMode, jsonOut bool) error {
	streams := iostream.FromCmd(cmd)
	root := resolveHookRoot(projectDir)

	report, err := watchd.FetchConflicts(root)
	if err != nil {
		reportConflictLookupError(streams, err, hookMode, jsonOut)
		return nil
	}

	if jsonOut {
		enc := json.NewEncoder(streams.Out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return nil
	}

	if !hookMode {
		streams.Printf("%s", watchd.ConflictStatus(report))
		return nil
	}

	notice := watchd.ConflictNotice(report)
	if notice == "" {
		// Nothing to advise. No output at all, so a hook firing on every commit
		// in a project that merges cleanly adds nothing to the transcript.
		return nil
	}
	// The notice goes to both channels on purpose: stdout as JSON is what the
	// agent consumes, stderr as text is what a person reading the transcript
	// sees. They cannot be combined — plain text on stdout would stop the JSON
	// parsing that carries it to the agent.
	streams.ErrPrintf("%s", notice)
	out := hookOutput{HookSpecificOutput: hookSpecificOutput{
		HookEventName:     "PreToolUse",
		AdditionalContext: notice,
	}}
	enc := json.NewEncoder(streams.Out)
	_ = enc.Encode(out)
	return nil
}

// reportConflictLookupError explains a missing answer, but only where an
// explanation is wanted. A hook stays silent: the daemon is optional, and a
// hook that announced its absence would fire on every commit for everyone not
// running `chunk watch`.
func reportConflictLookupError(streams iostream.Streams, err error, hookMode, jsonOut bool) {
	if hookMode {
		return
	}
	if jsonOut {
		_ = json.NewEncoder(streams.Out).Encode(map[string]string{"error": err.Error()})
		return
	}
	if errors.Is(err, watchd.ErrDaemonUnreachable) {
		streams.Printf("%s", "No watch daemon is running, so there is no conflict information.\n"+
			"Run `chunk watch` to start it.\n")
		return
	}
	streams.Printf("Conflict information is unavailable: %v\n", err)
}
