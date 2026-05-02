package skills

import (
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
		Description: `Run build/test/validate on a remote chunk sidecar instead of locally. Use when asked to "validate on the sidecar", "run tests on the sidecar", "sync to sidecar", "check this on the sidecar", or when edits need remote verification. Also covers creating sidecars, snapshots, and env customization.`,
	},
}

// Agent represents a target agent with its config directories.
type Agent struct {
	Name      string
	ConfigDir string // parent config dir (must exist for install)
	SkillsDir string // where skill subdirectories live
}

// Agents returns the list of supported agents for the given home directory.
func Agents(homeDir string) []Agent {
	return []Agent{
		{
			Name:      "claude",
			ConfigDir: filepath.Join(homeDir, ".claude"),
			SkillsDir: filepath.Join(homeDir, ".claude", "skills"),
		},
		{
			Name:      "codex",
			ConfigDir: filepath.Join(homeDir, ".agents"),
			SkillsDir: filepath.Join(homeDir, ".agents", "skills"),
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
}

// Install installs all embedded skills for agents whose config dirs exist.
// Agents with missing config dirs are skipped.
func Install(homeDir string) []AgentInstallResult {
	agents := Agents(homeDir)
	results := make([]AgentInstallResult, 0, len(agents))
	for _, agent := range agents {
		results = append(results, installForAgent(agent, All))
	}
	return results
}

// InstallByName installs a single skill by name for agents whose config dirs exist.
// Returns nil if the skill name is not found.
func InstallByName(homeDir, name string) []AgentInstallResult {
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
	agents := Agents(homeDir)
	results := make([]AgentInstallResult, 0, len(agents))
	for _, agent := range agents {
		results = append(results, installForAgent(agent, []Skill{*s}))
	}
	return results
}

func installForAgent(agent Agent, subset []Skill) AgentInstallResult {
	if _, err := os.Stat(agent.ConfigDir); os.IsNotExist(err) {
		return AgentInstallResult{Agent: agent.Name, Skipped: true, Installed: make([]string, 0), Updated: make([]string, 0)}
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

// Status returns per-agent, per-skill installation state without modifying anything.
func Status(homeDir string) []AgentStatus {
	agents := Agents(homeDir)
	results := make([]AgentStatus, 0, len(agents))

	for _, agent := range agents {
		available := true
		if _, err := os.Stat(agent.ConfigDir); os.IsNotExist(err) {
			available = false
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
