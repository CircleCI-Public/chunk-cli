package hook

import (
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// ExecConfig holds per-exec configuration.
type ExecConfig struct {
	Command string
	FileExt string
	Always  bool
	Timeout int
	Limit   int
}

// TaskConfig holds task command configuration.
type TaskConfig struct {
	Instructions string
	Schema       string
	Limit        int
	Always       bool
	Timeout      int
}

// ResolvedConfig is the merged configuration ready for use by commands.
type ResolvedConfig struct {
	Triggers    map[string][]string
	Execs       map[string]ExecConfig
	Tasks       map[string]TaskConfig
	SentinelDir string
	ProjectDir  string
}

// LoadConfig reads and resolves config from the project directory.
func LoadConfig(projectDir string) *ResolvedConfig {
	if projectDir == "" {
		projectDir, _ = os.Getwd()
	}

	cfg, _ := config.LoadProjectConfig(projectDir)
	if cfg == nil {
		cfg = &config.ProjectConfig{}
	}

	triggers := map[string][]string{
		"pre-commit": {"git commit", "git push"},
	}
	for name, patterns := range cfg.Triggers {
		triggers[name] = patterns
	}

	execs := map[string]ExecConfig{}
	for _, cmd := range cfg.Commands {
		timeout := cmd.Timeout
		if timeout == 0 {
			timeout = 300
		}
		execs[cmd.Name] = ExecConfig{
			Command: cmd.Run,
			FileExt: cmd.FileExt,
			Always:  cmd.Always,
			Timeout: timeout,
			Limit:   cmd.Limit,
		}
	}

	tasks := map[string]TaskConfig{}
	for name, t := range cfg.Tasks {
		limit := t.Limit
		if limit == 0 {
			limit = 3
		}
		timeout := t.Timeout
		if timeout == 0 {
			timeout = 600
		}
		tasks[name] = TaskConfig{
			Instructions: t.Instructions,
			Schema:       t.Schema,
			Limit:        limit,
			Always:       t.Always,
			Timeout:      timeout,
		}
	}

	sentinelDir := SentinelsDir()
	if sentinelDir == "" {
		tmp := os.TempDir()
		sentinelDir = filepath.Join(tmp, "chunk-hook", "sentinels")
	}

	return &ResolvedConfig{
		Triggers:    triggers,
		Execs:       execs,
		Tasks:       tasks,
		SentinelDir: sentinelDir,
		ProjectDir:  projectDir,
	}
}
