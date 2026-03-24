package hook

import (
	"fmt"
	"strings"
)

// CommandSpec is a parsed spec like "exec:tests" or "task:review".
type CommandSpec struct {
	Type string // "exec" or "task"
	Name string
}

// ParseSpecs parses command specifiers from positional arguments.
func ParseSpecs(args []string) ([]CommandSpec, error) {
	var specs []CommandSpec
	for _, arg := range args {
		idx := strings.Index(arg, ":")
		if idx == -1 {
			return nil, fmt.Errorf("invalid spec %q: expected type:name", arg)
		}
		typ := arg[:idx]
		name := arg[idx+1:]
		if name == "" {
			return nil, fmt.Errorf("invalid spec %q: empty name", arg)
		}
		if typ != "exec" && typ != "task" {
			return nil, fmt.Errorf("invalid spec type %q: must be exec or task", typ)
		}
		specs = append(specs, CommandSpec{Type: typ, Name: name})
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("no specs provided")
	}
	return specs, nil
}

// SyncCheckFlags holds parsed flags for sync check.
type SyncCheckFlags struct {
	Specs   []CommandSpec
	On      string
	Trigger string
	Matcher string
	Limit   int
	Staged  bool
	Always  bool
	OnFail  string
	Bail    bool
}

// RunSyncCheck performs a grouped sequential check.
func RunSyncCheck(cfg *ResolvedConfig, flags SyncCheckFlags) error {
	// Check global enable — any spec name will do
	enabled := false
	for _, spec := range flags.Specs {
		if IsEnabled(spec.Name) {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil // Not enabled, exit 0
	}

	// Full implementation would walk specs and check sentinels.
	return nil
}
