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
