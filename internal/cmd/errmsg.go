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
	msgMalformedProjectConfig   = "Could not read .chunk/config.json."
	msgUnreadableProjectConfig  = "Could not open .chunk/config.json."

	msgMalformedUserConfig = "Could not read the chunk config file."

	detailMalformedProjectConfig = "The file exists but is not valid JSON, so writing to it would discard its contents."
	suggestionFixProjectConfig   = "Fix the JSON syntax in .chunk/config.json, then run this command again."

	detailMalformedUserConfig = "The file exists but is not valid JSON, so writing to it would discard its contents."
	suggestionFixUserConfig   = "Fix the JSON syntax in the chunk config file, then run this command again."

	suggestionCheckPerms   = "Check file permissions."
	suggestionNetworkRetry = "Check your network connection and try again."
	suggestionGitRepo      = "Run this command from inside a git repo."
)
