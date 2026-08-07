package skills

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/skills"
)

// Scope determines where skills are installed.
type Scope string

const (
	// ScopeUser installs into the user's agent config directories (~/.claude, ~/.agents).
	// Agents whose config directories do not exist are skipped.
	ScopeUser Scope = "user"
	// ScopeProject installs into the project's agent config directories (.claude, .agents).
	// Directories are created as needed; no pre-existing config dir is required.
	ScopeProject Scope = "project"
)

// State describes the installation state of a skill for a specific agent.
type State string

// Skill installation states.
const (
	StateMissing  State = "missing"
	StateCurrent  State = "current"
	StateOutdated State = "outdated"
)

// Skill defines an embedded skill with its metadata.
type Skill struct {
	Name        string
	Description string
}

// All is the ordered list of bundled skills.
var All = []Skill{
	{
		Name:        "chunk-testing-gaps",
		Description: `Use when asked to "find testing gaps", "chunk testing-gaps", "mutation test", "mutate this code", or "find surviving mutants". Runs a 4-stage mutation testing process.`,
	},
	{
		Name:        "chunk-review",
		Description: `Use when asked to "review recent changes", "chunk review", "review my diff", "review this PR", or "review my changes". Applies team-specific review standards from .chunk/review-prompt.md.`,
	},
	{
		Name:        "debug-ci-failures",
		Description: `Debug CircleCI build failures, analyze test results, and identify flaky tests. Use when asked to "debug CI", "why is CI failing", "fix CI failures", "find flaky tests", or "check CircleCI".`,
	},
	{
		Name:        "chunk-sidecar",
		Description: `Run build/test/validate on a remote chunk sidecar instead of locally. Use when asked to "validate on the sidecar", "run tests on the sidecar", "sync to sidecar", "check this on the sidecar", "run smarter testing doctor", "diagnose smarter testing", or when edits need remote verification. Also covers creating sidecars, snapshots, and env customization.`,
	},
}

// Agent represents a target agent with its config directories.
type Agent struct {
	Name         string
	ConfigDir    string // parent config dir
	SkillsDir    string // where skill subdirectories live
	SkipIfAbsent bool   // when true, skip install if ConfigDir does not exist
}

// agents returns the list of supported agents for the given scope and base directory.
// For ScopeUser, baseDir is the user's home directory.
// For ScopeProject, baseDir is the project root directory.
func agents(scope Scope, baseDir string) []Agent {
	skipIfAbsent := scope == ScopeUser
	return []Agent{
		{
			Name:         "claude",
			ConfigDir:    filepath.Join(baseDir, ".claude"),
			SkillsDir:    filepath.Join(baseDir, ".claude", "skills"),
			SkipIfAbsent: skipIfAbsent,
		},
		{
			Name:         "codex",
			ConfigDir:    filepath.Join(baseDir, ".agents"),
			SkillsDir:    filepath.Join(baseDir, ".agents", "skills"),
			SkipIfAbsent: skipIfAbsent,
		},
	}
}

// SkillState checks the installation state of a skill for an agent.
func SkillState(skillsDir string, s Skill) State {
	path := filepath.Join(skillsDir, s.Name, "SKILL.md")
	existing, err := os.ReadFile(path)
	if err != nil {
		return StateMissing
	}
	embedded, err := skills.Content.ReadFile(filepath.Join(s.Name, "SKILL.md"))
	if err != nil {
		return StateMissing
	}
	if string(existing) == string(embedded) {
		return StateCurrent
	}
	return StateOutdated
}

// AgentInstallResult reports what happened for one agent during install.
type AgentInstallResult struct {
	Agent     string   `json:"agent"`
	Skipped   bool     `json:"skipped"`
	Installed []string `json:"installed"`
	Updated   []string `json:"updated"`
	Errors    []string `json:"errors,omitempty"`
}

// Install installs all embedded skills for the given scope and base directory.
// For ScopeUser, agents whose config dirs do not exist are skipped.
// For ScopeProject, dirs are created as needed.
func Install(scope Scope, baseDir string) []AgentInstallResult {
	all := agents(scope, baseDir)
	results := make([]AgentInstallResult, 0, len(all))
	for _, agent := range all {
		results = append(results, installForAgent(agent, All))
	}
	return results
}

// InstallByName installs a single skill by name.
// Returns nil if the skill name is not found.
func InstallByName(scope Scope, baseDir, name string) []AgentInstallResult {
	var s *Skill
	for i := range All {
		if All[i].Name == name {
			s = &All[i]
			break
		}
	}
	if s == nil {
		return nil
	}
	all := agents(scope, baseDir)
	results := make([]AgentInstallResult, 0, len(all))
	for _, agent := range all {
		results = append(results, installForAgent(agent, []Skill{*s}))
	}
	return results
}

func installForAgent(agent Agent, subset []Skill) AgentInstallResult {
	if agent.SkipIfAbsent {
		if _, err := os.Stat(agent.ConfigDir); err != nil {
			return AgentInstallResult{Agent: agent.Name, Skipped: true, Installed: make([]string, 0), Updated: make([]string, 0)}
		}
	}

	result := AgentInstallResult{Agent: agent.Name, Installed: make([]string, 0), Updated: make([]string, 0)}

	for _, s := range subset {
		state := SkillState(agent.SkillsDir, s)
		if state == StateCurrent {
			continue
		}

		data, err := skills.Content.ReadFile(filepath.Join(s.Name, "SKILL.md"))
		if err != nil {
			continue
		}

		dir := filepath.Join(agent.SkillsDir, s.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("create dir %s: %v", dir, err))
			continue
		}
		dest := filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("write %s: %v", dest, err))
			continue
		}

		if state == StateMissing {
			result.Installed = append(result.Installed, s.Name)
		} else {
			result.Updated = append(result.Updated, s.Name)
		}
	}
	return result
}

// AgentSkillStatus describes the state of a single skill for an agent.
type AgentSkillStatus struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	State       State  `json:"state"`
}

// AgentStatus describes per-agent availability and skill states.
type AgentStatus struct {
	Agent     string             `json:"agent"`
	Available bool               `json:"available"`
	Skills    []AgentSkillStatus `json:"skills"`
}

// Status returns per-agent, per-skill installation state without modifying anything.
// For ScopeUser, an agent is available only when its config dir exists.
// For ScopeProject, agents are always considered available.
func Status(scope Scope, baseDir string) []AgentStatus {
	all := agents(scope, baseDir)
	results := make([]AgentStatus, 0, len(all))

	for _, agent := range all {
		available := true
		if agent.SkipIfAbsent {
			if _, err := os.Stat(agent.ConfigDir); err != nil {
				available = false
			}
		}

		ss := make([]AgentSkillStatus, 0, len(All))
		for _, s := range All {
			state := StateMissing
			if available {
				state = SkillState(agent.SkillsDir, s)
			}
			ss = append(ss, AgentSkillStatus{
				Name:        s.Name,
				Description: s.Description,
				State:       state,
			})
		}
		results = append(results, AgentStatus{
			Agent:     agent.Name,
			Available: available,
			Skills:    ss,
		})
	}
	return results
}
