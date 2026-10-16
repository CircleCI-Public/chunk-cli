package validate

import (
	"fmt"
	"os"
	"path/filepath"
)

// TestSuitesTemplate returns the contents of .circleci/test-suites.yml for the
// detected toolchain in workDir, or "" if no toolchain template applies.
//
// The returned YAML targets CircleCI Smarter Testing: `<< test.atoms >>` is
// substituted at run time with the subset of test atoms picked by the platform.
func TestSuitesTemplate(workDir string) string {
	entries, _ := os.ReadDir(workDir)
	has := make(map[string]bool, len(entries))
	for _, e := range entries {
		has[e.Name()] = true
	}

	switch {
	case has["go.mod"]:
		return goTestSuitesYAML
	case has["pyproject.toml"]:
		return pytestTestSuitesYAML
	}
	return ""
}

// TestSuitesOutcome is what WriteTestSuites did.
// The zero value is no outcome, so an unset result never reads as success.
type TestSuitesOutcome int

const (
	// TestSuitesWritten means .circleci/test-suites.yml was written.
	TestSuitesWritten TestSuitesOutcome = iota + 1
	// TestSuitesExist means the project already had a test-suites.yml, which
	// was left alone.
	TestSuitesExist
	// TestSuitesNoTemplate means the project's toolchain has no template.
	TestSuitesNoTemplate
)

// WriteTestSuites scaffolds .circleci/test-suites.yml for CircleCI Smarter
// Testing when workDir's toolchain has a template, creating .circleci/ if need
// be. It never overwrites an existing test-suites.yml.
func WriteTestSuites(workDir string) (TestSuitesOutcome, error) {
	template := TestSuitesTemplate(workDir)
	if template == "" {
		return TestSuitesNoTemplate, nil
	}

	circleDir := filepath.Join(workDir, ".circleci")
	path := filepath.Join(circleDir, "test-suites.yml")
	if _, err := os.Stat(path); err == nil {
		return TestSuitesExist, nil
	}

	if err := os.MkdirAll(circleDir, 0o755); err != nil {
		return 0, fmt.Errorf("create .circleci dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return 0, fmt.Errorf("write test-suites.yml: %w", err)
	}
	return TestSuitesWritten, nil
}

const goTestSuitesYAML = `---
name: ci tests
discover: go list -f '{{ if or (len .TestGoFiles) (len .XTestGoFiles) }} {{ .ImportPath }} {{end}}' ./...
run: go tool gotestsum --junitfile="<< outputs.junit >>" -- -race << test.atoms >>
outputs:
  junit: test-reports/tests.xml
`

const pytestTestSuitesYAML = `---
name: ci tests
discover: python -m pytest --collect-only -q
run: python -m pytest --junit-xml=<< outputs.junit >> << test.atoms >>
outputs:
  junit: test-reports/tests.xml
`
