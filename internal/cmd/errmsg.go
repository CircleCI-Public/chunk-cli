package cmd

const (
	configFilePermHint          = "Check file permissions on the chunk config file."
	msgCouldNotLoadConfig       = "Could not load configuration."
	msgCouldNotAccessConfig     = "Could not access configuration."
	msgCouldNotDetermineWorkDir = "Could not determine working directory."
	msgCouldNotLoadSidecar      = "Could not load the active sidecar."
	msgHomeNotSet               = "HOME environment variable is not set."
	errMsgHomeNotSet            = "HOME not set"
	msgValidateNotConfigured    = "No validate commands configured."
	msgEmptyToken               = "Token cannot be empty."
	msgCouldNotSaveCredentials  = "Could not save credentials."

	suggestionCheckPerms   = "Check file permissions."
	suggestionNetworkRetry = "Check your network connection and try again."
	suggestionGitRepo      = "Run this command from inside a git repo."
	suggestionRunInit      = "Run 'chunk init' first."
)
