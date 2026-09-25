// Package review runs a directory of prompts as independent reviews, each on
// its own sidecar.
package review

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// DefaultDir is where prompts are read from, relative to the project root,
// when no directory is given.
const DefaultDir = ".chunk/reviews"

// promptExts are the file extensions read as prompts. Anything else in the
// directory is ignored, so it can hold fixtures without them becoming reviews.
var promptExts = []string{".md", ".txt"}

// ErrNoPrompts is returned when a prompts directory holds nothing to review.
var ErrNoPrompts = errors.New("no prompts found")

// Prompt is one review to run. Name is the file name without its extension and
// identifies the review in output across passes.
type Prompt struct {
	Name string
	Body string
}

// LoadPrompts reads every prompt file directly inside dir, sorted by name so
// runs are reproducible. Subdirectories are not descended into, and files that
// are empty after trimming whitespace are skipped rather than run as blank
// reviews.
func LoadPrompts(dir string) ([]Prompt, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read prompts directory: %w", err)
	}

	var prompts []Prompt
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if !slices.Contains(promptExts, strings.ToLower(ext)) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read prompt %s: %w", e.Name(), err)
		}
		body := strings.TrimSpace(string(data))
		if body == "" {
			continue
		}
		prompts = append(prompts, Prompt{Name: strings.TrimSuffix(e.Name(), ext), Body: body})
	}
	if len(prompts) == 0 {
		return nil, ErrNoPrompts
	}
	return prompts, nil
}

// PoolSize picks how many sidecars to boot: one per prompt, capped at the
// requested parallelism. Booting more sidecars than prompts only bills idle
// machines, since each review occupies exactly one sidecar.
func PoolSize(parallelism, prompts int) int {
	if parallelism <= 0 {
		return prompts
	}
	return min(parallelism, prompts)
}
