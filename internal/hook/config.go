package hook

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the top-level shape matching .chunk/hook/config.yml.
type Config struct {
	Triggers  map[string]TriggerConfig `yaml:"triggers"`
	Execs     map[string]ExecConfig    `yaml:"execs"`
	Tasks     map[string]TaskConfig    `yaml:"tasks"`
	Sentinels *SentinelsConfig         `yaml:"sentinels"`
}

// TriggerConfig holds trigger group patterns.
type TriggerConfig struct {
	Patterns []string `yaml:"patterns"`
}

// ExecConfig holds per-exec configuration from the YAML execs section.
type ExecConfig struct {
	Command string `yaml:"command"`
	FileExt string `yaml:"fileExt"`
	Always  bool   `yaml:"always"`
	Timeout int    `yaml:"timeout"`
	Limit   int    `yaml:"limit"`
}

// TaskConfig holds task command configuration.
type TaskConfig struct {
	Instructions string `yaml:"instructions"`
	Schema       string `yaml:"schema"`
	Limit        int    `yaml:"limit"`
	Always       bool   `yaml:"always"`
	Timeout      int    `yaml:"timeout"`
}

// SentinelsConfig holds sentinel directory configuration.
type SentinelsConfig struct {
	Dir string `yaml:"dir"`
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

	raw := readConfigFile(projectDir)

	triggers := map[string][]string{
		"pre-commit": {"git commit", "git push"},
	}
	if raw.Triggers != nil {
		for name, tc := range raw.Triggers {
			triggers[name] = tc.Patterns
		}
	}

	execs := map[string]ExecConfig{}
	if raw.Execs != nil {
		for name, cfg := range raw.Execs {
			if cfg.Timeout == 0 {
				cfg.Timeout = 300
			}
			execs[name] = cfg
		}
	}

	tasks := map[string]TaskConfig{}
	if raw.Tasks != nil {
		for name, cfg := range raw.Tasks {
			if cfg.Limit == 0 {
				cfg.Limit = 3
			}
			if cfg.Timeout == 0 {
				cfg.Timeout = 600
			}
			tasks[name] = cfg
		}
	}

	sentinelDir := SentinelsDir()
	if sentinelDir == "" {
		if raw.Sentinels != nil && raw.Sentinels.Dir != "" {
			sentinelDir = raw.Sentinels.Dir
		} else {
			tmp := os.TempDir()
			sentinelDir = filepath.Join(tmp, "chunk-hook", "sentinels")
		}
	}

	return &ResolvedConfig{
		Triggers:    triggers,
		Execs:       execs,
		Tasks:       tasks,
		SentinelDir: sentinelDir,
		ProjectDir:  projectDir,
	}
}

func readConfigFile(projectDir string) Config {
	configPath := os.Getenv("CHUNK_HOOK_CONFIG")
	if configPath == "" {
		configPath = filepath.Join(projectDir, ".chunk", "hook", "config.yml")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}
	}
	return cfg
}
