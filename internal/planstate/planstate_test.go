package planstate

import (
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestParse(t *testing.T) {
	cases := []struct {
		state string
		phase string
		actor Actor
	}{
		{
			state: "Planning - Human",
			phase: "Planning",
			actor: Human,
		},
		{
			state: "Building - Agent",
			phase: "Building",
			actor: Agent,
		},
		{
			state: " integrating - agent ",
			phase: "integrating",
			actor: Agent,
		},
		{
			state: "Review - HUMAN",
			phase: "Review",
			actor: Human,
		},
		{
			state: "Unprioritized",
			phase: "Unprioritized",
			actor: Unknown,
		},
		{
			state: "Blocked",
			phase: "Blocked",
			actor: Unknown,
		},
		{
			state: "Integrating - Bot",
			phase: "Integrating - Bot",
			actor: Unknown,
		},
		{
			state: "Waiting - On - Human",
			phase: "Waiting - On",
			actor: Human,
		},
		{
			state: "",
			phase: "",
			actor: Unknown,
		},
		{
			state: " - Human",
			phase: "- Human",
			actor: Unknown,
		},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			phase, actor := Parse(c.state)
			assert.Check(t, cmp.Equal(phase, c.phase))
			assert.Check(t, cmp.Equal(actor, c.actor))
		})
	}
}

func TestIsUnprioritized(t *testing.T) {
	assert.Check(t, IsUnprioritized("Unprioritized"))
	assert.Check(t, IsUnprioritized(" unprioritized "))
	assert.Check(t, !IsUnprioritized("Unprioritized - Human"))
	assert.Check(t, !IsUnprioritized(""))
}
