package factory

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// templateFS holds the standard review prompts for new projects, one directory
// per set, such as "go-service", and one markdown file per review in it.
//
//go:embed templates
var templateFS embed.FS

// ErrUnknownTemplate means no standard set of review prompts has the name.
var ErrUnknownTemplate = errors.New("unknown review template")

// Templates names the standard sets of review prompts, sorted.
func Templates() []string {
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		// The directory is embedded, so this cannot fail at run time.
		panic(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names
}

// Template returns the review prompts in the named standard set, sorted by
// name, ready for WritePrompts.
func Template(name string) ([]GeneratedPrompt, error) {
	if !slices.Contains(Templates(), name) {
		return nil, fmt.Errorf("%w %q", ErrUnknownTemplate, name)
	}
	dir := path.Join("templates", name)
	entries, err := templateFS.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read template %s: %w", name, err)
	}
	var prompts []GeneratedPrompt
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".md" {
			continue
		}
		body, err := fs.ReadFile(templateFS, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read template %s: %w", name, err)
		}
		prompts = append(prompts, GeneratedPrompt{
			Name: strings.TrimSuffix(e.Name(), ".md"),
			Body: strings.TrimSpace(string(body)),
		})
	}
	return prompts, nil
}
