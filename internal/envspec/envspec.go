package envspec

import "github.com/go-json-experiment/json/jsontext"

// Step is a single named provisioning command in the sidecar setup sequence.
type Step struct {
	Name    string `json:"name"`
	Command string `json:"command"`

	// Extra holds object members this type does not model, so that rewriting
	// .chunk/config.json through it does not delete keys a user hand-added to a
	// setup step. Encoding requires encoding/json/v2; see config.SaveProjectConfig.
	Extra jsontext.Value `json:",embed"`
}

// Environment describes the detected tech stack and build configuration for a repository.
type Environment struct {
	Stack        string `json:"stack"`
	Setup        []Step `json:"setup"`
	Image        string `json:"image"`
	ImageVersion string `json:"image_version"`

	// Extra holds object members this type does not model. See Step.Extra.
	Extra jsontext.Value `json:",embed"`
}

// StepTest is the detected test command. It is not a provisioning step: it
// exists only as the input to the Dockerfile CMD that `chunk sidecar build`
// generates from a piped env spec, and is never run as part of the sidecar
// setup sequence. How a project's tests are actually invoked is user-owned
// config — see config.ProjectConfig.Commands.
const StepTest = "test"

// ForConfig returns a copy of e suitable for persisting to .chunk/config.json,
// with the StepTest step removed so that Setup contains only steps that are
// really run during setup.
//
// Callers that generate a Dockerfile must use the full env spec, not this
// copy: dropping StepTest drops the image's CMD.
func (e *Environment) ForConfig() *Environment {
	if e == nil {
		return nil
	}
	out := *e
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
