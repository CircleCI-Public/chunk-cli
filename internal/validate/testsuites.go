package validate

import "os"

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
