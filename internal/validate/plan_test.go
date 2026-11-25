package validate

import (
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestPlanCommands(t *testing.T) {
	commands := []config.Command{
		{Name: "format", Local: true},
		{Name: "build"},
		{Name: "lint", Remote: true},
		{Name: "test", Remote: true},
	}

	t.Run("remote places every command on one worker", func(t *testing.T) {
		plan := PlanCommands(commands, PlacementRemote, 1)
		assert.Equal(t, len(plan.LocalCommands), 0)
		assert.Equal(t, len(plan.RemoteCommands), 4)
		assert.Equal(t, plan.PoolSize, 1)
	})

	t.Run("local requires no pool", func(t *testing.T) {
		plan := PlanCommands(commands, PlacementLocal, 10)
		assert.Equal(t, len(plan.LocalCommands), 4)
		assert.Equal(t, len(plan.RemoteCommands), 0)
		assert.Equal(t, plan.PoolSize, 0)
	})

	t.Run("configured defaults unspecified commands to remote", func(t *testing.T) {
		plan := PlanCommands(commands, PlacementConfigured, 10)
		assert.Equal(t, len(plan.LocalCommands), 1)
		assert.Equal(t, len(plan.RemoteCommands), 3)
		assert.Equal(t, plan.RemoteCommands[0].Name, "build")
		assert.Equal(t, plan.PoolSize, 3)
	})

	t.Run("configured placement does not infer locality from role", func(t *testing.T) {
		plan := PlanCommands([]config.Command{{Name: "format", Role: config.RoleAutofix}}, PlacementConfigured, 1)
		assert.Equal(t, len(plan.LocalCommands), 0)
		assert.Equal(t, len(plan.RemoteCommands), 1)
	})

	t.Run("remote workers default to one", func(t *testing.T) {
		plan := PlanCommands(commands, PlacementConfigured, 0)
		assert.Equal(t, plan.PoolSize, 1)
	})
	t.Run("a parallel command gets no slot in the shared pool", func(t *testing.T) {
		plan := PlanCommands([]config.Command{{Name: "lint"}, {Name: "test", Parallelism: 5}}, PlacementConfigured, 3)
		assert.Check(t, cmp.Len(plan.RemoteCommands, 1))
		assert.Check(t, cmp.Len(plan.ParallelCommands, 1))
		assert.Check(t, cmp.Equal(plan.PoolSize, 1))
	})

	t.Run("only parallel commands need no shared pool", func(t *testing.T) {
		plan := PlanCommands([]config.Command{{Name: "test", Parallelism: 5}}, PlacementConfigured, 3)
		assert.Check(t, cmp.Equal(plan.PoolSize, 0))
		assert.Check(t, cmp.Len(plan.ParallelCommands, 1))
	})

	t.Run("parallelism 1 runs once on the shared pool", func(t *testing.T) {
		plan := PlanCommands([]config.Command{{Name: "test", Parallelism: 1}}, PlacementConfigured, 3)
		assert.Check(t, cmp.Len(plan.RemoteCommands, 1))
		assert.Check(t, cmp.Len(plan.ParallelCommands, 0))
	})

	t.Run("a local parallel command stays local", func(t *testing.T) {
		plan := PlanCommands([]config.Command{{Name: "test", Local: true, Parallelism: 3}}, PlacementConfigured, 3)
		assert.Check(t, cmp.Len(plan.LocalCommands, 1))
		assert.Check(t, cmp.Len(plan.ParallelCommands, 0))
	})
}
