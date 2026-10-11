// Package brand is the one place the project's name lives. Everything that
// carries the name onto a user's machine (the command, folders, environment
// variables, keychain entries, Docker labels, branch names, image tags) is
// derived from it, so a rename is a change to this file.
//
// Some strings that look like the name are CircleCI API contract (the OAuth
// client ID, the task trigger source) and stay where they are, marked in place.
package brand

// Command is the binary and the name users type.
const Command = "smelt"

// Names derived from Command. They are constants rather than computed so they
// can appear in const blocks and struct tags.
const (
	// ProjectDir is the per-repository folder holding config, reviews and state.
	ProjectDir = ".smelt"

	// HomeDir is the folder under the user's home directory for the daemon
	// and factory run logs. It matches ProjectDir by design.
	HomeDir = ".smelt"

	// AppDir is the folder name under the XDG config, data and state homes and
	// under the user cache directory.
	AppDir = "smelt"

	// EnvPrefix starts every environment variable the project reads.
	EnvPrefix = "SMELT_"

	// KeychainPrefix starts every keychain service name.
	KeychainPrefix = "smelt:"

	// KeychainFallbackUser is the keychain account name when the OS user
	// cannot be determined.
	KeychainFallbackUser = "smelt"

	// LabelPrefix starts every Docker label key.
	LabelPrefix = "smelt."

	// BranchPrefix starts every branch the factory creates and publishes.
	BranchPrefix = "smelt/"

	// Dockerfile is the sandbox Dockerfile written into a repository.
	Dockerfile = "Dockerfile.smelt"

	// ImagePrefix starts every sandbox image tag.
	ImagePrefix = "smelt-"
)

// Env returns the environment variable name for a setting, e.g.
// Env("DAEMON_DIR") is "SMELT_DAEMON_DIR".
func Env(name string) string {
	return EnvPrefix + name
}

// Label returns the Docker label key for a setting, e.g. Label("factory") is
// "smelt.factory".
func Label(name string) string {
	return LabelPrefix + name
}
