package initialize

import (
	"context"
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/hook"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/skills"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

// Options controls which steps are executed during initialization.
type Options struct {
	WorkDir      string
	HomeDir      string
	Profile      string
	Force        bool
	SkipHooks    bool
	SkipValidate bool
	SkipCircleCI bool
	SkipSkills   bool
}

// Result holds the outcome of a completed initialization.
type Result struct {
	Org             string
	Repo            string
	CircleCIOrgName string
	Commands        []config.Command
	PackageManager  string
	HooksSetUp      bool
	SkillResults    []skills.AgentInstallResult
}

// Run executes the full initialization sequence and returns a summary of
// what was set up. Progress messages are written to streams.Err.
func Run(ctx context.Context, opts Options, streams iostream.Streams) (*Result, error) {
	if _, err := gitutil.RepoRoot(opts.WorkDir); err != nil {
		return nil, fmt.Errorf("not a git repository, run this command from inside a git repo")
	}

	if err := hook.ValidateProfile(opts.Profile); err != nil {
		return nil, err
	}

	// Guard: exit cleanly if config exists and --force not set
	existingCfg, loadErr := config.LoadProjectConfig(opts.WorkDir)
	if loadErr == nil && !opts.Force {
		hasData := existingCfg.HasCommands() || existingCfg.VCS != nil || existingCfg.CircleCI != nil
		if hasData {
			streams.ErrPrintln("Config already exists at .chunk/config.json")
			streams.ErrPrintln(ui.Dim("To overwrite: chunk init --force"))
			return nil, nil
		}
	}

	// Seed from existing config when --force so skipped sections are preserved.
	cfg := &config.ProjectConfig{}
	if opts.Force && loadErr == nil {
		cfg = existingCfg
	}

	result := &Result{}

	// Step 1: VCS config from git remote
	org, repo, err := gitremote.DetectOrgAndRepo(opts.WorkDir)
	if err != nil {
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not detect VCS info: %v", err)))
	} else {
		cfg.VCS = &config.VCSConfig{Org: org, Repo: repo}
		result.Org = org
		result.Repo = repo
		streams.ErrPrintf("Detected repository: %s\n", ui.Bold(fmt.Sprintf("%s/%s", org, repo)))
	}

	// Step 2: CircleCI org setup
	if !opts.SkipCircleCI {
		setupCircleCI(ctx, cfg, result, streams)
	}

	// Step 3: Validate command detection
	if !opts.SkipValidate {
		detectCommands(ctx, opts.WorkDir, cfg, result, streams)
	}

	// Save config
	if err := config.SaveProjectConfig(opts.WorkDir, cfg); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}
	streams.ErrPrintln(ui.Success("Wrote .chunk/config.json"))

	// Step 4: Hook setup
	if !opts.SkipHooks {
		projectName := ""
		if cfg.VCS != nil && cfg.VCS.Repo != "" {
			projectName = cfg.VCS.Repo
		}
		if err := hook.RunSetup(opts.WorkDir, projectName, opts.Profile, opts.Force, false, "", cfg.Commands, streams); err != nil {
			return nil, fmt.Errorf("hook setup: %w", err)
		}
		result.HooksSetUp = true
	}

	// Step 5: Skill installation
	if !opts.SkipSkills && opts.HomeDir != "" {
		result.SkillResults = skills.Install(opts.HomeDir)
	}

	return result, nil
}

func setupCircleCI(ctx context.Context, cfg *config.ProjectConfig, result *Result, streams iostream.Streams) {
	var orgID, orgName string

	client, clientErr := circleci.NewClient()
	if clientErr == nil {
		streams.ErrPrintln(ui.Dim("Fetching CircleCI organizations..."))
		collabs, listErr := client.ListCollaborations(ctx)
		if listErr == nil && len(collabs) > 0 {
			items := make([]string, len(collabs))
			for i, c := range collabs {
				items[i] = c.Name
			}
			idx, selectErr := tui.SelectFromList("Select CircleCI organization:", items)
			if selectErr == nil {
				orgID = collabs[idx].ID
				orgName = collabs[idx].Name
			}
		}
	}

	// Fallback: manual org ID entry
	if orgID == "" {
		streams.ErrPrintln(ui.Dim("Enter your CircleCI organization ID (UUID from Organization Settings):"))
		val, promptErr := tui.PromptText("Organization ID", "")
		if promptErr == nil && val != "" {
			orgID = val
		}
	}

	if orgID != "" {
		cfg.CircleCI = &config.CircleCIConfig{OrgID: orgID}
		if orgName != "" {
			result.CircleCIOrgName = orgName
			streams.ErrPrintf("Selected organization: %s\n", ui.Bold(orgName))
		} else {
			result.CircleCIOrgName = orgID
			streams.ErrPrintf("Set organization ID: %s\n", ui.Bold(orgID))
		}
	} else {
		streams.ErrPrintln(ui.Warning("Skipping CircleCI org setup"))
	}
}

func detectCommands(ctx context.Context, workDir string, cfg *config.ProjectConfig, result *Result, streams iostream.Streams) {
	claude, _ := anthropic.New() // nil if unavailable — static detection works without it
	commands, detectErr := validate.DetectCommands(ctx, claude, workDir)
	if detectErr != nil {
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not detect commands: %v", detectErr)))
		return
	}

	var allCommands []config.Command
	pm := validate.DetectPackageManager(workDir)
	if pm != nil {
		result.PackageManager = pm.Name
		streams.ErrPrintf("Detected package manager: %s\n", ui.Bold(pm.Name))
		allCommands = append(allCommands, config.Command{Name: "install", Run: pm.InstallCommand})
	}
	allCommands = append(allCommands, commands...)
	cfg.Commands = allCommands
	result.Commands = commands
	for _, c := range commands {
		streams.ErrPrintf("Detected command: %s (%s)\n", ui.Bold(c.Name), ui.Gray(c.Run))
	}
}
