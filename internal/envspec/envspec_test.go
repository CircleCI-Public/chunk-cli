package envspec

import (
	"testing"

	"github.com/go-json-experiment/json/jsontext"
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

// A detected environment carries no extras of its own, so replacing the saved
// block with one has to inherit the keys the user hand-added or they are lost on
// the next `sidecar env` / `sidecar setup`.
func TestWithExtrasFrom(t *testing.T) {
	t.Parallel()

	prev := &Environment{
		Stack: "go",
		Setup: []Step{
			{Name: "install", Command: "go mod download", Extra: jsontext.Value(`{"cache":true}`)},
			{Name: "gone", Command: "old", Extra: jsontext.Value(`{"stale":1}`)},
		},
		Extra: jsontext.Value(`{"cacheKey":"v1"}`),
	}

	t.Run("carries environment and step extras", func(t *testing.T) {
		t.Parallel()
		detected := &Environment{
			Stack: "go",
			Setup: []Step{
				{Name: "install", Command: "go mod download"},
				{Name: "build", Command: "go build ./..."},
			},
		}

		got := detected.WithExtrasFrom(prev)
		assert.Equal(t, string(got.Extra), `{"cacheKey":"v1"}`)
		assert.Equal(t, string(got.Setup[0].Extra), `{"cache":true}`)

		// A step only the detected spec has keeps its own (absent) extras, and a
		// step that disappeared takes its extras with it.
		assert.Equal(t, len(got.Setup), 2)
		assert.Equal(t, len(got.Setup[1].Extra), 0)

		// The detected spec is not mutated — callers still hold it for the
		// Dockerfile.
		assert.Equal(t, len(detected.Extra), 0)
		assert.Equal(t, len(detected.Setup[0].Extra), 0)
	})

	t.Run("does not overwrite extras the receiver already has", func(t *testing.T) {
		t.Parallel()
		detected := &Environment{
			Setup: []Step{{Name: "install", Extra: jsontext.Value(`{"cache":false}`)}},
			Extra: jsontext.Value(`{"cacheKey":"v2"}`),
		}

		got := detected.WithExtrasFrom(prev)
		assert.Equal(t, string(got.Extra), `{"cacheKey":"v2"}`)
		assert.Equal(t, string(got.Setup[0].Extra), `{"cache":false}`)
	})

	t.Run("nil receiver or nil previous", func(t *testing.T) {
		t.Parallel()
		assert.Assert(t, (*Environment)(nil).WithExtrasFrom(prev) == nil)

		detected := &Environment{Stack: "go"}
		assert.Equal(t, detected.WithExtrasFrom(nil), detected)
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
