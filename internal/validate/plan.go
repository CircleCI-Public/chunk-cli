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
	// ParallelCommands are remote commands with Parallelism of 2 or more; each
	// runs on a pool of its own, of that size, apart from RemoteCommands.
	ParallelCommands []config.Command
}

// PlanCommands assigns commands to local or remote execution and caps the
// remote worker count to the amount of remote work available.
func PlanCommands(commands []config.Command, placement Placement, maxRemoteWorkers int) Plan {
	var plan Plan
	switch placement {
	case PlacementRemote:
		for _, command := range commands {
			plan.addRemote(command)
		}
	case PlacementLocal:
		plan.LocalCommands = append(plan.LocalCommands, commands...)
	case PlacementConfigured:
		// Roles describe what commands do; they do not implicitly place them.
		// In particular, legacy autofix commands without local: true are not
		// migrated here: unspecified placement intentionally defaults to remote.
		for _, command := range commands {
			if command.RunsLocally() {
				plan.LocalCommands = append(plan.LocalCommands, command)
			} else {
				plan.addRemote(command)
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

// addRemote files a command placed remote under the shared batch, or under
// ParallelCommands when it runs more than once.
func (p *Plan) addRemote(command config.Command) {
	if command.Parallelism >= 2 {
		p.ParallelCommands = append(p.ParallelCommands, command)
		return
	}
	p.RemoteCommands = append(p.RemoteCommands, command)
}
