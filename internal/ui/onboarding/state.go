package onboarding

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// State is onboarding's saved progress for a project, in
// config.RepoDataDir(root)/onboarding.json.
type State struct {
	PlanID string `json:"plan_id,omitempty"`
	// Suggestion is the title of the suggestion PlanID was started from, ""
	// for a change the developer described, so an unmerged end can mark it
	// unused again.
	Suggestion  string       `json:"suggestion,omitempty"`
	Left        bool         `json:"left,omitempty"`     // esc → Go to flows
	Finished    bool         `json:"finished,omitempty"` // the plan's flow ended merged
	Suggestions []Suggestion `json:"suggestions"`
}

// Suggestion is a change onboarding offers to start with, and whether a plan
// was started from it.
type Suggestion struct {
	Title string `json:"title"`
	Used  bool   `json:"used"`
}

// Generic are the changes onboarding offers to start with.
var Generic = []Suggestion{{Title: "Add keyboard shortcuts"}, {Title: "Export a table to CSV"}, {Title: "Add dark mode"}}

// Fresh is the state of a project onboarding has not started on.
func Fresh() State { return State{Suggestions: slices.Clone(Generic)} }

// Unused is the titles of s's suggestions no plan was started from, in order.
func (s State) Unused() []string {
	var out []string
	for _, sg := range s.Suggestions {
		if !sg.Used {
			out = append(out, sg.Title)
		}
	}
	return out
}

// StateStore keeps onboarding's State for a project.
type StateStore interface {
	Load() State // a missing or corrupt file gives State{Suggestions: Generic}
	Save(State) error
}

// FileStore is the StateStore at config.RepoDataDir(root)/onboarding.json.
func FileStore(root string) (StateStore, error) {
	dir, err := config.RepoDataDir(root)
	if err != nil {
		return nil, fmt.Errorf("find onboarding's data directory: %w", err)
	}
	return fileStore{path: filepath.Join(dir, "onboarding.json")}, nil
}

type fileStore struct{ path string }

func (f fileStore) Load() State {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return Fresh()
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return Fresh()
	}
	if s.Suggestions == nil {
		s.Suggestions = slices.Clone(Generic)
	}
	return s
}

func (f fileStore) Save(s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode onboarding state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return fmt.Errorf("create onboarding's data directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), filepath.Base(f.path)+".tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	werr := tmp.Chmod(0o600)
	if werr == nil {
		_, werr = tmp.Write(append(data, '\n'))
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write onboarding state: %w", werr)
	}
	if err := os.Rename(tmp.Name(), f.path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("save onboarding state: %w", err)
	}
	return nil
}
