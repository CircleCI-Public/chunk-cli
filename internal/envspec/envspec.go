package envspec

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
