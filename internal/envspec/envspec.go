package envspec

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// Step is a single named provisioning command in the sidecar setup sequence.
type Step struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

// Environment describes the detected tech stack and build configuration for a repository.
type Environment struct {
	Stack        string `json:"stack"`
	Setup        []Step `json:"setup"`
	Image        string `json:"image"`
	ImageVersion string `json:"image_version"`
	// TestSuite is the detected CircleCI Smarter Testing suite that the
	// StepTest command runs. Like that step it is only an input to the
	// Dockerfile `chunk sidecar build` generates from a piped env spec, so it
	// is not persisted.
	TestSuite *TestSuite `json:"test_suite,omitempty"`
}

// TestSuite is one suite of .circleci/test-suites.yml, run with
// `circleci testsuite run "<Name>"`. Run receives the discovered test atoms
// through `<< test.atoms >>`.
type TestSuite struct {
	Name     string `json:"name"`
	Discover string `json:"discover"`
	Run      string `json:"run"`
}

// YAML returns s as a test-suites.yml document, starting with its "---"
// separator so it can be appended to an existing file.
func (s *TestSuite) YAML() string {
	return "\n---\nname: " + strconv.Quote(s.Name) +
		"\ndiscover: " + strconv.Quote(s.Discover) +
		"\nrun: " + strconv.Quote(s.Run) + "\n"
}

// RunCommand is the shell command that runs s.
func (s *TestSuite) RunCommand() string {
	return "circleci testsuite run '" + strings.ReplaceAll(s.Name, "'", `'\''`) + "'"
}

// AppendCommand is a shell command that appends s to .circleci/test-suites.yml
// in the working directory, keeping any suites the project already has. The
// YAML is base64-encoded so it survives a single Dockerfile RUN line intact.
func (s *TestSuite) AppendCommand() string {
	return "mkdir -p .circleci && echo " + base64.StdEncoding.EncodeToString([]byte(s.YAML())) +
		" | base64 -d >> .circleci/test-suites.yml"
}

// StepTest is the detected test command, which runs Environment.TestSuite
// with `circleci testsuite run`. It is not a provisioning step: it exists only
// as the input to the Dockerfile CMD that `chunk sidecar build` generates from
// a piped env spec, and is never run as part of the sidecar setup sequence.
// How a project's tests are actually invoked is user-owned config — see
// config.ProjectConfig.Commands.
const StepTest = "test"

// ForConfig returns a copy of e suitable for persisting to .chunk/config.json,
// with the StepTest step and TestSuite removed so that Setup contains only
// steps that are really run during setup.
//
// Callers that generate a Dockerfile must use the full env spec, not this
// copy: dropping StepTest drops the image's CMD.
func (e *Environment) ForConfig() *Environment {
	if e == nil {
		return nil
	}
	out := *e
	out.TestSuite = nil
	out.Setup = make([]Step, 0, len(e.Setup))
	for _, s := range e.Setup {
		if s.Name == StepTest {
			continue
		}
		out.Setup = append(out.Setup, s)
	}
	return &out
}

// ProvisioningSteps returns the number of steps that really run during setup,
// i.e. Setup without StepTest. Zero for a nil receiver.
func (e *Environment) ProvisioningSteps() int {
	if e == nil {
		return 0
	}
	n := 0
	for _, s := range e.Setup {
		if s.Name != StepTest {
			n++
		}
	}
	return n
}

// SetupStep returns the command for the named setup step, or "" if absent.
func (e *Environment) SetupStep(name string) string {
	for _, s := range e.Setup {
		if s.Name == name {
			return s.Command
		}
	}
	return ""
}

// BinaryPaths returns colon-separated PATH prefixes needed for the detected stack.
// cimg images set these via Docker ENV which e2b does not propagate to SSH sessions.
func (e *Environment) BinaryPaths() string {
	switch e.Stack {
	case "go":
		return "/usr/local/go/bin:/home/circleci/go/bin"
	case "javascript", "typescript":
		return "/home/circleci/.yarn/bin"
	default:
		return ""
	}
}
