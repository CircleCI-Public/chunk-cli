// Package all is the registry of the harnesses smelt can drive.
package all

import (
	"fmt"

	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/claude"
	"github.com/smeltdev/smelt/internal/harness/codex"
)

// All returns every harness, Claude first.
func All() []harness.Harness {
	return []harness.Harness{claude.Harness{}, codex.Harness{}}
}

// ByName returns the harness called name.
func ByName(name harness.Name) (harness.Harness, error) {
	for _, h := range All() {
		if h.Name() == name {
			return h, nil
		}
	}
	return nil, fmt.Errorf("unknown harness %q (choose claude or codex)", name)
}
