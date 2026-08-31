package skills

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Scope determines where the plugin is installed.
type Scope string

const (
	// ScopeUser installs the plugin at user scope (~/.claude).
	ScopeUser Scope = "user"
	// ScopeProject installs the plugin at project scope (.claude in the project root).
	ScopeProject Scope = "project"
)

const (
	// pluginName is the name of the CircleCI plugin in the marketplace.
	pluginName = "circleci"
	// agentClaude is the name of the Claude Code agent.
	agentClaude = "claude"
)

// AgentInstallResult reports the outcome of a plugin install attempt for one agent.
type AgentInstallResult struct {
	Agent     string   `json:"agent"`
	Skipped   bool     `json:"skipped"`
	Installed []string `json:"installed"`
	Updated   []string `json:"updated"`
	Errors    []string `json:"errors,omitempty"`
}

// State describes the installation state of a plugin for a specific agent.
type State string

const (
	// StateMissing means the plugin is not installed.
	StateMissing State = "missing"
	// StateCurrent means the plugin is installed and up to date.
	StateCurrent State = "current"
	// StateOutdated means the plugin is installed but a newer version is available.
	StateOutdated State = "outdated"
)

// AgentSkillStatus describes the state of the plugin for an agent.
type AgentSkillStatus struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	State       State  `json:"state"`
}

// AgentStatus describes per-agent plugin availability.
type AgentStatus struct {
	Agent     string             `json:"agent"`
	Available bool               `json:"available"`
	Skills    []AgentSkillStatus `json:"skills"`
}

// pluginEntry is one entry from `claude plugin list --json`.
type pluginEntry struct {
	ID      string `json:"id"`
	Scope   string `json:"scope"`
	Enabled bool   `json:"enabled"`
}

// Install installs the CircleCI plugin for Claude Code at the given scope.
// If the claude CLI is not found, the agent is reported as skipped.
func Install(scope Scope) []AgentInstallResult {
	return []AgentInstallResult{installForClaude(scope)}
}

// InstallByName is kept for call-site compatibility with chunk init.
// It installs the CircleCI plugin regardless of which skill names are requested.
func InstallByName(scope Scope, _ string, _ ...string) []AgentInstallResult {
	return Install(scope)
}

func installForClaude(scope Scope) AgentInstallResult {
	result := AgentInstallResult{
		Agent:     agentClaude,
		Installed: make([]string, 0),
		Updated:   make([]string, 0),
	}

	if _, err := exec.LookPath(agentClaude); err != nil {
		result.Skipped = true
		return result
	}

	args := []string{"plugin", "install", pluginName, "--yes", "--scope", string(scope)}
	out, err := exec.Command(agentClaude, args...).CombinedOutput() //nolint:gosec
	if err != nil {
		result.Errors = append(result.Errors, strings.TrimSpace(string(out)))
		return result
	}

	outStr := string(out)
	switch {
	case strings.Contains(outStr, "already installed"):
		result.Updated = append(result.Updated, pluginName)
	default:
		result.Installed = append(result.Installed, pluginName)
	}
	return result
}

// Status returns per-agent plugin installation state without modifying anything.
func Status(scope Scope, _ string) []AgentStatus {
	return []AgentStatus{statusForClaude(scope)}
}

func statusForClaude(scope Scope) AgentStatus {
	if _, err := exec.LookPath(agentClaude); err != nil {
		return AgentStatus{Agent: agentClaude, Available: false, Skills: pluginSkillStatuses(StateMissing)}
	}

	out, err := exec.Command(agentClaude, "plugin", "list", "--json").CombinedOutput() //nolint:gosec
	if err != nil {
		return AgentStatus{Agent: agentClaude, Available: true, Skills: pluginSkillStatuses(StateMissing)}
	}

	var entries []pluginEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return AgentStatus{Agent: agentClaude, Available: true, Skills: pluginSkillStatuses(StateMissing)}
	}

	state := StateMissing
	for _, e := range entries {
		name, _, _ := strings.Cut(e.ID, "@")
		if name == pluginName && e.Scope == string(scope) {
			state = StateCurrent
			break
		}
	}
	return AgentStatus{Agent: agentClaude, Available: true, Skills: pluginSkillStatuses(state)}
}

func pluginSkillStatuses(state State) []AgentSkillStatus {
	return []AgentSkillStatus{
		{
			Name:        pluginName,
			Description: "CircleCI plugin: chunk sidecar, review, testing-gaps, CI debugging",
			State:       state,
		},
	}
}

// Skill and All are kept for compatibility with callers that enumerate skills.
type Skill struct {
	Name        string
	Description string
}

// All lists the skills bundled in the CircleCI plugin.
var All = []Skill{
	{Name: "chunk-sidecar", Description: "Sync→validate dev loop on a remote CircleCI sidecar"},
	{Name: "chunk-sidecar-setup", Description: "Interactive onboarding wizard for first-time sidecar setup"},
	{Name: "chunk-review", Description: "Team-prompt-driven code review via subagent"},
	{Name: "chunk-testing-gaps", Description: "4-stage mutation testing on parallel throwaway sidecars"},
	{Name: "debug-ci-failures", Description: "Diagnose CircleCI pipeline failures and flaky tests"},
}

// SkillState is kept for compatibility but always reflects the plugin's overall state.
func SkillState(_ string, _ Skill) State {
	return StateMissing
}

// ErrSkillInstall is returned when the plugin install step fails.
func ErrSkillInstall(agent, msg string) error {
	return fmt.Errorf("%s: %s", agent, msg)
}
