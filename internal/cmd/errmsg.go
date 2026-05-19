package cmd

const (
	configFilePermHint          = "Check file permissions on the chunk config file."
	keychainHint                = "Keychain unavailable. Re-run with --insecure-storage to save credentials to the config file instead."
	msgCouldNotLoadConfig       = "Could not load configuration."
	msgCouldNotAccessConfig     = "Could not access configuration."
	msgCouldNotDetermineWorkDir = "Could not determine working directory."
	msgCouldNotLoadSidecar      = "Could not load the active sidecar."
	msgHomeNotSet               = "HOME environment variable is not set."
	errMsgHomeNotSet            = "HOME not set"

	suggestionCheckPerms   = "Check file permissions."
	suggestionNetworkRetry = "Check your network connection and try again."
	suggestionGitRepo      = "Run this command from inside a git repo."
)
