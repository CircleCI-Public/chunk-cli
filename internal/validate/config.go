package validate

import "github.com/CircleCI-Public/chunk-cli/internal/config"

// Command is an alias for config.Command.
type Command = config.Command

// ProjectConfig is an alias for config.ProjectConfig.
type ProjectConfig = config.ProjectConfig

// LoadProjectConfig is a re-export of config.LoadProjectConfig.
var LoadProjectConfig = config.LoadProjectConfig

// SaveProjectConfig is a re-export of config.SaveProjectConfig.
var SaveProjectConfig = config.SaveProjectConfig

// SaveCommand is a re-export of config.SaveCommand.
var SaveCommand = config.SaveCommand
