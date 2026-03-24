package skills

import "embed"

//go:embed chunk-review/SKILL.md chunk-testing-gaps/SKILL.md debug-ci-failures/SKILL.md
var Content embed.FS
