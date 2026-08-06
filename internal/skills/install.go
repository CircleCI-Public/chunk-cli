package skills

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/skills"
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

// Scope determines where skills are installed.
type Scope int

const (
	// ProjectScope targets agent config directories found in the current directory (.claude, .codex).
	ProjectScope Scope = iota
	// UserScope targets user-level agent directories ($HOME/.claude, $HOME/.agents).
	UserScope
)

// agent represents a target agent with its config directories.
type agent struct {
	name      string
	configDir string
	skillsDir string
}

// supportedProjectDirs maps dot directory names to agent names for project-level detection.
var supportedProjectDirs = []struct {
	dotDir string
	name   string
}{
	{".claude", "claude"},
	{".codex", "codex"},
}

// SupportedProjectDotDirs returns the dot directory names checked for project-level installs.
func SupportedProjectDotDirs() []string {
	names := make([]string, len(supportedProjectDirs))
	for i, d := range supportedProjectDirs {
		names[i] = d.dotDir
	}
	return names
}

func resolveAgents(scope Scope) ([]agent, error) {
	switch scope {
	case UserScope:
		home := os.Getenv("HOME")
		if home == "" {
			return nil, fmt.Errorf("HOME environment variable is not set")
		}
		return []agent{
			{name: "claude", configDir: filepath.Join(home, ".claude"), skillsDir: filepath.Join(home, ".claude", "skills")},
			{name: "codex", configDir: filepath.Join(home, ".agents"), skillsDir: filepath.Join(home, ".agents", "skills")},
		}, nil
	case ProjectScope:
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("could not determine current directory: %w", err)
		}
		var agents []agent
		for _, d := range supportedProjectDirs {
			configDir := filepath.Join(cwd, d.dotDir)
			if _, err := os.Stat(configDir); err == nil {
				agents = append(agents, agent{
					name:      d.name,
					configDir: configDir,
					skillsDir: filepath.Join(configDir, "skills"),
				})
			}
		}
		return agents, nil
	default:
		return nil, fmt.Errorf("unknown scope %d", scope)
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
}

// Install installs all embedded skills for the given scope.
func Install(scope Scope) ([]AgentInstallResult, error) {
	agents, err := resolveAgents(scope)
	if err != nil {
		return nil, err
	}
	results := make([]AgentInstallResult, 0, len(agents))
	for _, a := range agents {
		results = append(results, installForAgent(a, All))
	}
	return results, nil
}

// InstallByName installs a single skill by name for the given scope.
// Returns nil if the skill name is not found.
func InstallByName(scope Scope, name string) ([]AgentInstallResult, error) {
	var s *Skill
	for i := range All {
		if All[i].Name == name {
			s = &All[i]
			break
		}
	}
	if s == nil {
		return nil, nil
	}
	agents, err := resolveAgents(scope)
	if err != nil {
		return nil, err
	}
	results := make([]AgentInstallResult, 0, len(agents))
	for _, a := range agents {
		results = append(results, installForAgent(a, []Skill{*s}))
	}
	return results, nil
}

func installForAgent(a agent, subset []Skill) AgentInstallResult {
	if _, err := os.Stat(a.configDir); os.IsNotExist(err) {
		return AgentInstallResult{Agent: a.name, Skipped: true, Installed: make([]string, 0), Updated: make([]string, 0)}
	}

	result := AgentInstallResult{Agent: a.name, Installed: make([]string, 0), Updated: make([]string, 0)}

	for _, s := range subset {
		state := SkillState(a.skillsDir, s)
		if state == StateCurrent {
			continue
		}

		data, err := skills.Content.ReadFile(filepath.Join(s.Name, "SKILL.md"))
		if err != nil {
			continue
		}

		dir := filepath.Join(a.skillsDir, s.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		dest := filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(dest, data, 0o644); err != nil {
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

// Status returns per-agent, per-skill installation state for the given scope.
func Status(scope Scope) ([]AgentStatus, error) {
	agents, err := resolveAgents(scope)
	if err != nil {
		return nil, err
	}
	results := make([]AgentStatus, 0, len(agents))
	for _, a := range agents {
		available := true
		if _, err := os.Stat(a.configDir); os.IsNotExist(err) {
			available = false
		}
		ss := make([]AgentSkillStatus, 0, len(All))
		for _, s := range All {
			state := StateMissing
			if available {
				state = SkillState(a.skillsDir, s)
			}
			ss = append(ss, AgentSkillStatus{
				Name:        s.Name,
				Description: s.Description,
				State:       state,
			})
		}
		results = append(results, AgentStatus{
			Agent:     a.name,
			Available: available,
			Skills:    ss,
		})
	}
	return results, nil
}
