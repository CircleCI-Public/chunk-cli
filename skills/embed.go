package skills

import "embed"

//go:embed smelt-review/SKILL.md smelt-testing-gaps/SKILL.md smelt-sidecar/SKILL.md smelt-sidecar-setup/SKILL.md debug-ci-failures/SKILL.md smelt-validate-config/SKILL.md

// Content holds the embedded skill definition files.
var Content embed.FS
