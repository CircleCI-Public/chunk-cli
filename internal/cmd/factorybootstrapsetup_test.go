package cmd

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
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
