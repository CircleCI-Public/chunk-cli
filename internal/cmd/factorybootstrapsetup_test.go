package cmd

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func TestPlanBootstrap(t *testing.T) {
	withImage := &config.ProjectConfig{Validation: &config.ValidationConfig{SidecarImage: "snap-1"}}
	tests := []struct {
		name        string
		cfg         *config.ProjectConfig
		promptsErr  error
		skipSidecar bool
		want        bootstrapPlan
		wantSkipped []string
		empty       bool
	}{
		{
			name: "a new project gets everything",
			want: bootstrapPlan{Config: true, Sidecar: true, Prompts: true, History: true},
		},
		{
			name:        "an existing config is kept",
			cfg:         &config.ProjectConfig{},
			want:        bootstrapPlan{Sidecar: true, Prompts: true, History: true},
			wantSkipped: []string{".chunk/config.json already exists"},
		},
		{
			name:        "a recorded image is kept",
			cfg:         withImage,
			want:        bootstrapPlan{Prompts: true, History: true},
			wantSkipped: []string{".chunk/config.json already exists", "sidecar image: validation.sidecarImage is snap-1"},
		},
		{
			name:        "--skip-sidecar leaves the image out",
			skipSidecar: true,
			want:        bootstrapPlan{Config: true, Prompts: true, History: true},
			wantSkipped: []string{"sidecar image: --skip-sidecar"},
		},
		{
			name:        "existing prompts are kept",
			cfg:         &config.ProjectConfig{},
			promptsErr:  factory.ErrPromptsExist,
			want:        bootstrapPlan{Sidecar: true, History: true},
			wantSkipped: []string{".chunk/config.json already exists", ".chunk/reviews already has review prompts"},
		},
		{
			name:        "a project set up already has nothing to do",
			cfg:         withImage,
			promptsErr:  factory.ErrPromptsExist,
			want:        bootstrapPlan{History: true},
			wantSkipped: []string{".chunk/config.json already exists", "sidecar image: validation.sidecarImage is snap-1", ".chunk/reviews already has review prompts"},
			empty:       true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planBootstrap(tt.cfg, tt.promptsErr, tt.skipSidecar, false, ".chunk/reviews")
			assert.DeepEqual(t, got.skipped, tt.wantSkipped)
			got.skipped = nil
			assert.DeepEqual(t, got, tt.want, cmp.AllowUnexported(bootstrapPlan{}))
			assert.Equal(t, got.empty(), tt.empty)
		})
	}
}

func TestRunConcurrentlyRunsEachAndMarksItsLines(t *testing.T) {
	// Each part waits for the other to start, so the test only finishes if
	// they run at the same time.
	var started sync.WaitGroup
	started.Add(2)
	part := func(name string, err error) bootstrapPart {
		return bootstrapPart{name: name, run: func(_ context.Context, status iostream.StatusFunc) error {
			started.Done()
			started.Wait()
			status(iostream.LevelStep, "working")
			return err
		}}
	}
	failed := errors.New("sidecar failed")
	var mu sync.Mutex
	var lines []string
	status := func(_ iostream.Level, msg string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, msg)
	}

	errs := runConcurrently(context.Background(), []bootstrapPart{part("sidecar", failed), part("prompts", nil)}, status)

	assert.Equal(t, len(errs), 2)
	assert.Equal(t, errs[0], failed)
	assert.NilError(t, errs[1])
	slices.Sort(lines)
	assert.DeepEqual(t, lines, []string{"[prompts] working", "[sidecar] working"})
}

func TestRunConcurrentlyLeavesALoneLineUnmarked(t *testing.T) {
	var lines []string
	runConcurrently(context.Background(), []bootstrapPart{{name: "prompts", run: func(_ context.Context, status iostream.StatusFunc) error {
		status(iostream.LevelStep, "working")
		return nil
	}}}, func(_ iostream.Level, msg string) { lines = append(lines, msg) })

	assert.DeepEqual(t, lines, []string{"working"})
}

func TestBootstrapPartsErrorWarnsOfAllButTheLast(t *testing.T) {
	parts := []bootstrapPart{{name: "sidecar"}, {name: "prompts"}}
	var errOut strings.Builder
	streams := iostream.Streams{Err: &errOut}

	err := bootstrapPartsError(streams, parts, []error{errors.New("no sidecar"), errors.New("no prompts")})

	assert.Error(t, err, "no prompts")
	assert.Assert(t, strings.Contains(errOut.String(), "[sidecar] no sidecar"), "stderr: %s", errOut.String())
	assert.NilError(t, bootstrapPartsError(streams, parts, []error{nil, nil}))
}

func TestLastLines(t *testing.T) {
	assert.Assert(t, lastLines("", 2) == nil)
	assert.DeepEqual(t, lastLines("a\nb\nc\n", 2), []string{"b", "c"})
	assert.DeepEqual(t, lastLines("a\n", 2), []string{"a"})
}
