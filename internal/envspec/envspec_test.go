package envspec

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestForConfig(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()
		assert.Assert(t, (*Environment)(nil).ForConfig() == nil)
	})

	t.Run("drops the test step and keeps the rest in order", func(t *testing.T) {
		t.Parallel()
		env := &Environment{
			Stack: "go",
			Setup: []Step{
				{Name: "system", Command: "apt-get install git"},
				{Name: "install", Command: "go mod download"},
				{Name: StepTest, Command: "go test -p 1 ./..."},
			},
			Image:        "cimg/go",
			ImageVersion: "1.26.2",
		}

		got := env.ForConfig()
		assert.DeepEqual(t, got.Setup, []Step{
			{Name: "system", Command: "apt-get install git"},
			{Name: "install", Command: "go mod download"},
		})
		assert.Equal(t, got.Stack, "go")
		assert.Equal(t, got.Image, "cimg/go")
		assert.Equal(t, got.ImageVersion, "1.26.2")

		// The receiver keeps its test step so the Dockerfile CMD still has it.
		assert.Equal(t, len(env.Setup), 3)
		assert.Equal(t, env.SetupStep(StepTest), "go test -p 1 ./...")
	})

	t.Run("no test step is a no-op", func(t *testing.T) {
		t.Parallel()
		env := &Environment{Setup: []Step{{Name: "install", Command: "npm ci"}}}
		assert.DeepEqual(t, env.ForConfig().Setup, env.Setup)
	})

	t.Run("empty setup", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, len((&Environment{}).ForConfig().Setup), 0)
	})
}

func TestProvisioningSteps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  *Environment
		want int
	}{
		{name: "nil receiver", env: nil, want: 0},
		{name: "empty setup", env: &Environment{}, want: 0},
		{
			name: "test step is not counted",
			env: &Environment{Setup: []Step{
				{Name: "system", Command: "apt-get install git"},
				{Name: "install", Command: "go mod download"},
				{Name: StepTest, Command: "go test ./..."},
			}},
			want: 2,
		},
		{
			name: "only a test step",
			env:  &Environment{Setup: []Step{{Name: StepTest, Command: "go test ./..."}}},
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.env.ProvisioningSteps(), tc.want)
			if tc.env != nil {
				// Always agrees with the filtered copy written to config.
				assert.Equal(t, tc.env.ProvisioningSteps(), len(tc.env.ForConfig().Setup))
			}
		})
	}
}
