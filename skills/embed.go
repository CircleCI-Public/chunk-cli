package skills

import "embed"

//go:embed chunk-review/SKILL.md chunk-testing-gaps/SKILL.md chunk-sidecar/SKILL.md debug-ci-failures/SKILL.md

// Content holds the embedded skill definition files.
var Content embed.FS
