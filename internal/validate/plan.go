package validate

import "github.com/CircleCI-Public/chunk-cli/internal/config"

// Placement controls where validation commands execute.
type Placement uint8

const (
	// PlacementRemote runs every selected command remotely.
	PlacementRemote Placement = iota
	// PlacementLocal runs every selected command locally.
	PlacementLocal
	// PlacementConfigured honors explicit local placement and defaults to remote.
	PlacementConfigured
)

// Plan separates command placement from the number of remote workers needed.
type Plan struct {
	LocalCommands  []config.Command
	RemoteCommands []config.Command
	PoolSize       int
}

// PlanCommands assigns commands to local or remote execution and caps the
// remote worker count to the amount of remote work available.
func PlanCommands(commands []config.Command, placement Placement, maxRemoteWorkers int) Plan {
	var plan Plan
	switch placement {
	case PlacementRemote:
		plan.RemoteCommands = append(plan.RemoteCommands, commands...)
	case PlacementLocal:
		plan.LocalCommands = append(plan.LocalCommands, commands...)
	case PlacementConfigured:
		for _, command := range commands {
			if command.RunsLocally() {
				plan.LocalCommands = append(plan.LocalCommands, command)
			} else {
				plan.RemoteCommands = append(plan.RemoteCommands, command)
			}
		}
	}

	if len(plan.RemoteCommands) == 0 {
		return plan
	}
	if maxRemoteWorkers < 1 {
		maxRemoteWorkers = 1
	}
	plan.PoolSize = min(maxRemoteWorkers, len(plan.RemoteCommands))
	return plan
}
