package cmd

import (
	"bufio"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// newFactoryAgentCmd runs factory.Agent, the session behind the planning
// screen (chunk factory plan), by hand, for trying it outside the screen.
func newFactoryAgentCmd() *cobra.Command {
	var resume, pluginDir, model string

	cmd := &cobra.Command{
		Use:          "agent",
		Short:        "Run a long-lived factory planning session (for trying it by hand)",
		Hidden:       true,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context() // cancelled on SIGINT by main.go
			streams := iostream.FromCmd(cmd)
			root, err := sessionProjectRoot(ctx)
			if err != nil {
				return err
			}
			cfg := factory.AgentConfig{Dir: root, Model: model, PluginDir: pluginDir, SessionID: resume, Resume: resume != ""}
			if cfg.SessionID == "" {
				cfg.SessionID = uuid.NewString()
			}
			// claude starts on its first message, so the session waits for
			// one.
			lines := bufio.NewScanner(cmd.InOrStdin())
			if !lines.Scan() {
				return nil
			}
			a, err := factory.StartAgent(ctx, cfg, lines.Text())
			if err != nil {
				return agentStartError(err)
			}
			streams.ErrPrintf("session %s\n", cfg.SessionID)
			go sendAgentLines(lines, a)
			for ev := range a.Events() {
				printAgentEvent(streams, ev)
			}
			return a.Stop()
		},
	}

	cmd.Flags().StringVar(&resume, "resume", "", "session ID to reopen")
	cmd.Flags().StringVar(&pluginDir, "plugin-dir", "", "load the f plugin from this directory instead of the installed one")
	cmd.Flags().StringVar(&model, "model", "", "model (default: your Claude Code default)")
	return cmd
}

// sendAgentLines sends each line from lines to a as a message, until input
// ends (Ctrl-D) or a send fails, either of which ends the session.
func sendAgentLines(lines *bufio.Scanner, a *factory.Agent) {
	for lines.Scan() {
		if err := a.Send(lines.Text()); err != nil {
			break
		}
	}
	_ = a.Stop()
}

// printAgentEvent shows one AgentEvent: an activity to stdout, a turn's reply
// to stdout (prefixed with [task] when claude started it on its own, on a
// factory task's notification), or a problem to stderr.
func printAgentEvent(streams iostream.Streams, ev factory.AgentEvent) {
	switch {
	case ev.Activity != nil:
		streams.Println("· " + ev.Activity.Line())
	case ev.TurnEnded != nil:
		reply := ev.TurnEnded.Reply
		if ev.TurnEnded.ByTask {
			reply = "[task] " + reply
		}
		streams.Println(reply)
	case ev.Problem != nil:
		streams.ErrPrintln("problem: " + ev.Problem.Error())
	}
}

// agentStartError maps StartAgent's sentinel errors to actionable guidance.
func agentStartError(err error) error {
	switch {
	case errors.Is(err, factory.ErrNoClaude):
		return newUserError("Claude Code is not installed on this machine.").
			withSuggestion("Install it from https://claude.com/claude-code.").
			withoutDetail()
	case errors.Is(err, factory.ErrNoFactory):
		return newUserError("factory is not installed.").
			withSuggestion("Install factory and put it on PATH.").
			withoutDetail()
	case errors.Is(err, factory.ErrNoFactoryDaemon):
		return newUserError("The factory daemon is not running.").
			withSuggestion("Run 'factory daemon' in a terminal, with FACTORY_PLAN_STORE=circleci and FACTORY_PLAN_CIRCLECI_TOKEN set to a token for the CircleCI user chunk is signed in as.").
			withoutDetail()
	case errors.Is(err, factory.ErrNoFactoryPlugin):
		return newUserError("factory's f plugin is not loaded.").
			withSuggestion("Install factory's f plugin, or pass --plugin-dir.").
			withoutDetail()
	case errors.Is(err, factory.ErrFactoryMCP):
		return newUserError("factory's MCP server is not connected.").
			withSuggestion("Make sure factory is on PATH.").
			withoutDetail()
	}
	return fmt.Errorf("start the factory agent: %w", err)
}
