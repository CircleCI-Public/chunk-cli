package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/gitutil"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/claude"
	"github.com/smeltdev/smelt/internal/harness/status"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/sandboximage"
	"github.com/smeltdev/smelt/internal/ui"
)

// statusCmdName is the subcommand that reports.
const statusCmdName = "status"

func newHarnessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "harness",
		Short: "Inspect the coding agents factory runs can use",
		RunE:  groupRunE,
	}
	cmd.AddCommand(newHarnessStatusCmd())
	return cmd
}

func newHarnessStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   statusCmdName,
		Short: "Show which agents are installed and logged in, and their usage limits",
		Long: `Show, for Claude Code and Codex: whether the CLI is installed on this machine and
in the sandbox image, whether its login works, the plan, and the best usage
data available.

Codex's figures come from its own app-server (the data its /status shows). For
Claude, smelt reads Claude Code's own login from the macOS keychain, read-only,
and asks Anthropic's usage endpoint. That endpoint is undocumented, and
Anthropic has said third-party tools should not call it; turn the probe off
with "harness": {"claude": {"usage_probe": false}} in ~/.config/smelt/config.json.
Without it, or when it fails, the last rate-limit signal a run saw is shown.

smelt only reports; choosing between agents is up to the caller.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)
			rep := status.Build(ctx, harnessStatusDeps(ctx))
			if jsonOut {
				enc := json.NewEncoder(streams.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}
			printHarnessStatus(streams, rep)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// harnessStatusDeps gathers what Build looks at: the project's default agent
// and image, when run inside a project, and the user's probe preference.
func harnessStatusDeps(ctx context.Context) status.Deps {
	var d status.Deps
	d.Dir, _ = status.Dir()
	// An unreadable user config leaves the probe off rather than on: the user
	// may have turned it off there.
	if user, err := config.Load(); err == nil && user.ClaudeUsageProbe() {
		d.UsageProbe = &claude.UsageProbe{CachePath: filepath.Join(d.Dir, "claude.json")}
	}
	return projectDeps(ctx, d)
}

// projectDeps fills in the default agent and the image's agents from the
// project in the working directory, if there is one.
func projectDeps(ctx context.Context, d status.Deps) status.Deps {
	dir, err := os.Getwd()
	if err != nil {
		return d
	}
	root := gitutil.TopLevelCtx(ctx, dir)
	if root == "" {
		return d
	}
	cfg, err := config.LoadProjectConfig(root)
	if err != nil || cfg.Factory == nil {
		return d
	}
	d.Default = cfg.Factory.Harness
	d.ImageHarnesses = imageHarnesses(ctx, factoryImageTag(root, cfg.Factory))
	return d
}

// factoryImageTag is the docker image the project's factory runs use, "" when
// it is not known without building anything.
func factoryImageTag(root string, f *config.FactoryConfig) string {
	switch {
	case f.Image != "":
		return f.Image
	case f.Dockerfile != "":
		if plan, err := sandboximage.NewPlan(root, f.Dockerfile); err == nil {
			return plan.Tag
		}
	}
	return ""
}

func printHarnessStatus(streams iostream.Streams, rep status.Report) {
	if rep.Default != "" {
		streams.Printf("Default agent: %s\n\n", rep.Default)
	}
	for _, e := range rep.Harnesses {
		streams.Println(ui.Bold(string(e.Name)))
		streams.Printf("  installed:  %s on this machine, %s in the image\n", yesNo(e.InstalledHost), yesNo(e.InstalledImage))
		login := e.Auth
		if e.AuthKind != "" {
			login += " (" + e.AuthKind + ")"
		}
		streams.Printf("  login:      %s\n", login)
		if e.Plan != "" {
			streams.Printf("  plan:       %s\n", e.Plan)
		}
		for _, w := range e.RateLimits {
			streams.Println("  " + describeWindow(w))
		}
		if e.Note != "" {
			streams.Println(ui.Dim("  note: " + e.Note))
		}
		streams.Println("")
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// describeWindow is one usage window on a line.
func describeWindow(w harness.Window) string {
	name := w.Window
	if name == "" {
		name = "limit"
	}
	parts := []string{fmt.Sprintf("%-10s", name)}
	if w.UsedPercent != nil {
		parts = append(parts, fmt.Sprintf("%.0f%% used", *w.UsedPercent))
	}
	if w.Status != "" {
		parts = append(parts, w.Status)
	}
	if w.ResetsAt != nil {
		parts = append(parts, "resets "+w.ResetsAt.Local().Format("Mon 15:04"))
	}
	parts = append(parts, "["+w.Source+"]")
	return strings.Join(parts, "  ")
}
