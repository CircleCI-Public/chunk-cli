package cmd

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestPlanBootstrap(t *testing.T) {
	withImage := &config.ProjectConfig{Validation: &config.ValidationConfig{SidecarImage: "snap-1"}}
	const noToken = "the team's review comments: no GitHub token. Run 'chunk auth set github' to include them, or pass --no-history."
	tests := []struct {
		name        string
		in          planInputs
		want        bootstrapPlan
		wantSkipped []string
		empty       bool
	}{
		{
			name: "a new project gets everything",
			in:   planInputs{GitHubToken: true},
			want: bootstrapPlan{Config: true, Sidecar: true, Prompts: true, History: true},
		},
		{
			name:        "an existing config is kept",
			in:          planInputs{Cfg: &config.ProjectConfig{}, GitHubToken: true},
			want:        bootstrapPlan{Sidecar: true, Prompts: true, History: true},
			wantSkipped: []string{".chunk/config.json already exists"},
		},
		{
			name:        "a recorded image is kept",
			in:          planInputs{Cfg: withImage, GitHubToken: true},
			want:        bootstrapPlan{Prompts: true, History: true},
			wantSkipped: []string{".chunk/config.json already exists", "sidecar image: validation.sidecarImage is snap-1"},
		},
		{
			name:        "--eval-commits scores the prompts the run writes",
			in:          planInputs{Cfg: withImage, NoHistory: true, EvalCommits: 3},
			want:        bootstrapPlan{Prompts: true, Score: 3},
			wantSkipped: []string{".chunk/config.json already exists", "sidecar image: validation.sidecarImage is snap-1"},
		},
		{
			name:        "--eval-commits leaves existing prompts to factory eval",
			in:          planInputs{Cfg: withImage, PromptsExist: true, PromptsDir: ".chunk/reviews", EvalCommits: 3},
			wantSkipped: []string{".chunk/config.json already exists", "sidecar image: validation.sidecarImage is snap-1", ".chunk/reviews already has review prompts", "scoring: .chunk/reviews already has review prompts. Score them with chunk factory eval --commits 3."},
			empty:       true,
		},
		{
			name:        "--eval-commits needs an image --skip-sidecar will not record",
			in:          planInputs{Cfg: &config.ProjectConfig{}, SkipSidecar: true, NoHistory: true, EvalCommits: 3},
			want:        bootstrapPlan{Prompts: true},
			wantSkipped: []string{".chunk/config.json already exists", "sidecar image: --skip-sidecar", "scoring: --skip-sidecar, and validation.sidecarImage is not set to score the prompts on"},
		},
		{
			name:        "--skip-sidecar leaves the image out",
			in:          planInputs{SkipSidecar: true, GitHubToken: true},
			want:        bootstrapPlan{Config: true, Prompts: true, History: true},
			wantSkipped: []string{"sidecar image: --skip-sidecar"},
		},
		{
			name:        "without a GitHub token the plan says the comments are left out",
			in:          planInputs{},
			want:        bootstrapPlan{Config: true, Sidecar: true, Prompts: true},
			wantSkipped: []string{noToken},
		},
		{
			name: "--no-history needs no token and says nothing of it",
			in:   planInputs{NoHistory: true},
			want: bootstrapPlan{Config: true, Sidecar: true, Prompts: true},
		},
		{
			name:        "existing prompts are kept, and need no history",
			in:          planInputs{Cfg: &config.ProjectConfig{}, PromptsExist: true, PromptsDir: ".chunk/reviews"},
			want:        bootstrapPlan{Sidecar: true},
			wantSkipped: []string{".chunk/config.json already exists", ".chunk/reviews already has review prompts"},
		},
		{
			name:        "a project set up already has nothing to do",
			in:          planInputs{Cfg: withImage, PromptsExist: true, PromptsDir: ".chunk/reviews", GitHubToken: true},
			want:        bootstrapPlan{},
			wantSkipped: []string{".chunk/config.json already exists", "sidecar image: validation.sidecarImage is snap-1", ".chunk/reviews already has review prompts"},
			empty:       true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planBootstrap(tt.in)
			assert.DeepEqual(t, got.skipped, tt.wantSkipped)
			assert.Equal(t, got.Config, tt.want.Config)
			assert.Equal(t, got.Sidecar, tt.want.Sidecar)
			assert.Equal(t, got.Prompts, tt.want.Prompts)
			assert.Equal(t, got.History, tt.want.History)
			assert.Equal(t, got.Score, tt.want.Score)
			assert.Equal(t, got.empty(), tt.empty)
		})
	}
}
