package task

import (
	"context"
	"errors"
	"testing"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

// fakePrompts returns a Prompts struct that replays canned responses.
// selectResponses is consumed in order by SelectFrom calls.
// textResponses is consumed in order by PromptText calls.
// confirmResponses is consumed in order by Confirm calls.
func fakePrompts(
	selectResponses []int,
	textResponses []string,
	confirmResponses []bool,
	warnings *[]string,
) Prompts {
	si, ti, ci := 0, 0, 0
	return Prompts{
		SelectFrom: func(label string, items []string) (int, error) {
			if si >= len(selectResponses) {
				return -1, errors.New("unexpected SelectFrom call")
			}
			idx := selectResponses[si]
			si++
			return idx, nil
		},
		PromptText: func(label, defaultVal string) (string, error) {
			if ti >= len(textResponses) {
				return "", errors.New("unexpected PromptText call")
			}
			val := textResponses[ti]
			ti++
			if val == "" {
				return defaultVal, nil
			}
			return val, nil
		},
		Confirm: func(label string, defaultVal bool) (bool, error) {
			if ci >= len(confirmResponses) {
				return false, nil
			}
			val := confirmResponses[ci]
			ci++
			return val, nil
		},
		Warn: func(msg string) {
			if warnings != nil {
				*warnings = append(*warnings, msg)
			}
		},
	}
}

func fakeFetchDetail(orgID, projectID string) ProjectDetailFunc {
	return func(_ context.Context, slug string) (*circleci.ProjectDetail, error) {
		return &circleci.ProjectDetail{
			ID:    projectID,
			OrgID: orgID,
			Slug:  slug,
		}, nil
	}
}

func TestCollectRunConfig_SelectFromList(t *testing.T) {
	projects := []circleci.FollowedProject{
		{Username: "acme", Reponame: "api", VcsType: "github"},
	}
	// Select first project (index 0), then fill one definition, then decline more.
	prompts := fakePrompts(
		[]int{0},
		[]string{
			"dev",                                  // definition name
			"550e8400-e29b-41d4-a716-446655440000", // definition ID
			"",                                     // description (empty -> default)
			"",                                     // default branch (empty -> "main")
			"",                                     // environment ID (empty -> skip)
		},
		[]bool{false}, // don't add another
		nil,
	)

	cfg, err := CollectRunConfig(
		context.Background(),
		prompts,
		projects,
		nil,
		fakeFetchDetail("org-1", "proj-1"),
		"",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OrgID != "org-1" {
		t.Fatalf("OrgID = %q, want %q", cfg.OrgID, "org-1")
	}
	if cfg.ProjectID != "proj-1" {
		t.Fatalf("ProjectID = %q, want %q", cfg.ProjectID, "proj-1")
	}
	if cfg.OrgType != "github" {
		t.Fatalf("OrgType = %q, want %q", cfg.OrgType, "github")
	}
	if len(cfg.Definitions) != 1 {
		t.Fatalf("got %d definitions, want 1", len(cfg.Definitions))
	}
	dev := cfg.Definitions["dev"]
	if dev.DefinitionID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("DefinitionID = %q", dev.DefinitionID)
	}
	if dev.DefaultBranch != "main" {
		t.Fatalf("DefaultBranch = %q, want %q", dev.DefaultBranch, "main")
	}
	if dev.ChunkEnvironmentID != nil {
		t.Fatalf("ChunkEnvironmentID = %v, want nil", dev.ChunkEnvironmentID)
	}
}

func TestCollectRunConfig_ManualEntry(t *testing.T) {
	collabs := []circleci.Collaboration{
		{ID: "org-99", Name: "my-org", VcsType: "github"},
	}
	// Select "Enter manually" (index 0, but 0 projects so index 0 is manual).
	// Actually, with 0 projects, items = ["Enter manually"], index 0 triggers manual.
	prompts := fakePrompts(
		[]int{
			0, // "Enter manually" (only item since no projects)
			0, // select org index 0
		},
		[]string{
			"proj-manual",                          // project ID
			"prod",                                 // definition name
			"660e8400-e29b-41d4-a716-446655440000", // definition ID
			"production env",                       // description
			"release",                              // default branch
			"770e8400-e29b-41d4-a716-446655440000", // environment ID
		},
		[]bool{false}, // don't add another
		nil,
	)

	cfg, err := CollectRunConfig(
		context.Background(),
		prompts,
		nil, // no followed projects
		collabs,
		nil, // fetchDetail not needed for manual
		"",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OrgID != "org-99" {
		t.Fatalf("OrgID = %q, want %q", cfg.OrgID, "org-99")
	}
	if cfg.ProjectID != "proj-manual" {
		t.Fatalf("ProjectID = %q, want %q", cfg.ProjectID, "proj-manual")
	}
	if cfg.OrgType != "github" {
		t.Fatalf("OrgType = %q, want %q", cfg.OrgType, "github")
	}

	prod := cfg.Definitions["prod"]
	if prod.DefinitionID != "660e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("DefinitionID = %q", prod.DefinitionID)
	}
	if prod.Description != "production env" {
		t.Fatalf("Description = %q", prod.Description)
	}
	if prod.DefaultBranch != "release" {
		t.Fatalf("DefaultBranch = %q, want %q", prod.DefaultBranch, "release")
	}
	if prod.ChunkEnvironmentID == nil || *prod.ChunkEnvironmentID != "770e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("ChunkEnvironmentID = %v", prod.ChunkEnvironmentID)
	}
}

func TestCollectRunConfig_MultipleDefinitions(t *testing.T) {
	projects := []circleci.FollowedProject{
		{Username: "acme", Reponame: "web", VcsType: "github"},
	}
	prompts := fakePrompts(
		[]int{0}, // select project
		[]string{
			// first definition
			"dev",
			"aaaa1111-bbbb-cccc-dddd-eeeeeeee0001",
			"", // description
			"", // branch
			"", // env
			// second definition
			"prod",
			"aaaa1111-bbbb-cccc-dddd-eeeeeeee0002",
			"production",
			"release",
			"",
		},
		[]bool{true, false}, // add another, then stop
		nil,
	)

	cfg, err := CollectRunConfig(
		context.Background(),
		prompts,
		projects,
		nil,
		fakeFetchDetail("org-1", "proj-1"),
		"",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Definitions) != 2 {
		t.Fatalf("got %d definitions, want 2", len(cfg.Definitions))
	}
	if _, ok := cfg.Definitions["dev"]; !ok {
		t.Fatal("missing dev definition")
	}
	if _, ok := cfg.Definitions["prod"]; !ok {
		t.Fatal("missing prod definition")
	}
}

func TestCollectRunConfig_InvalidUUIDRetries(t *testing.T) {
	projects := []circleci.FollowedProject{
		{Username: "acme", Reponame: "api", VcsType: "github"},
	}
	var warnings []string
	prompts := fakePrompts(
		[]int{0},
		[]string{
			"dev",                                  // definition name
			"not-valid",                            // invalid UUID (triggers warning, retries)
			"550e8400-e29b-41d4-a716-446655440000", // valid UUID
			"",                                     // description
			"",                                     // branch
			"not-valid",                            // invalid env UUID (triggers warning, retries)
			"660e8400-e29b-41d4-a716-446655440000", // valid env UUID
		},
		[]bool{false},
		&warnings,
	)

	cfg, err := CollectRunConfig(
		context.Background(),
		prompts,
		projects,
		nil,
		fakeFetchDetail("org-1", "proj-1"),
		"",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 2 {
		t.Fatalf("got %d warnings, want 2", len(warnings))
	}
	dev := cfg.Definitions["dev"]
	if dev.DefinitionID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("DefinitionID = %q", dev.DefinitionID)
	}
	if dev.ChunkEnvironmentID == nil || *dev.ChunkEnvironmentID != "660e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("ChunkEnvironmentID = %v", dev.ChunkEnvironmentID)
	}
}

func TestCollectRunConfig_RequiredDefIDRetries(t *testing.T) {
	projects := []circleci.FollowedProject{
		{Username: "acme", Reponame: "api", VcsType: "github"},
	}
	var warnings []string

	si, ti, ci := 0, 0, 0
	selectResps := []int{0}
	// PromptText returns "" for empty, but the fakePrompts helper returns defaultVal on "".
	// We need a custom prompts to simulate truly empty input for the required UUID retry.
	textResps := []struct {
		val     string
		isEmpty bool
	}{
		{val: "dev"},    // definition name
		{isEmpty: true}, // empty definition ID -> required
		{val: "550e8400-e29b-41d4-a716-446655440000"}, // valid definition ID
		{val: ""}, // description (use default)
		{val: ""}, // branch (use default)
		{val: ""}, // env ID (skip)
	}
	confirmResps := []bool{false}

	prompts := Prompts{
		SelectFrom: func(label string, items []string) (int, error) {
			idx := selectResps[si]
			si++
			return idx, nil
		},
		PromptText: func(label, defaultVal string) (string, error) {
			r := textResps[ti]
			ti++
			if r.isEmpty {
				return "", nil
			}
			if r.val == "" {
				return defaultVal, nil
			}
			return r.val, nil
		},
		Confirm: func(label string, defaultVal bool) (bool, error) {
			val := confirmResps[ci]
			ci++
			return val, nil
		},
		Warn: func(msg string) {
			warnings = append(warnings, msg)
		},
	}

	cfg, err := CollectRunConfig(
		context.Background(),
		prompts,
		projects,
		nil,
		fakeFetchDetail("org-1", "proj-1"),
		"",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(warnings), warnings)
	}
	if warnings[0] != "  This field is required." {
		t.Fatalf("unexpected warning: %q", warnings[0])
	}
	dev := cfg.Definitions["dev"]
	if dev.DefinitionID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("DefinitionID = %q", dev.DefinitionID)
	}
	_ = cfg
}

func TestCollectRunConfig_NoCollabsError(t *testing.T) {
	// Select "Enter manually" with no collabs -> error
	prompts := fakePrompts(
		[]int{0},
		nil,
		nil,
		nil,
	)

	_, err := CollectRunConfig(
		context.Background(),
		prompts,
		nil, // no projects, so "Enter manually" is index 0
		nil, // no collabs
		nil,
		"",
	)
	if err == nil {
		t.Fatal("expected error for no organizations")
	}
}

func TestCollectRunConfig_OrgMismatchWarning(t *testing.T) {
	projects := []circleci.FollowedProject{
		{Username: "acme", Reponame: "api", VcsType: "github"},
	}
	var warnings []string
	prompts := fakePrompts(
		[]int{0},
		[]string{
			"dev",
			"550e8400-e29b-41d4-a716-446655440000",
			"", "", "",
		},
		[]bool{false},
		&warnings,
	)

	_, err := CollectRunConfig(
		context.Background(),
		prompts,
		projects,
		nil,
		fakeFetchDetail("org-1", "proj-1"),
		"different-org",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, w := range warnings {
		if w == "Warning: selected project org (org-1) differs from CIRCLECI_ORG_ID (different-org)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected org mismatch warning, got: %v", warnings)
	}
}

func TestCollectRunConfig_BitbucketPrefix(t *testing.T) {
	projects := []circleci.FollowedProject{
		{Username: "acme", Reponame: "api", VcsType: "Bitbucket"},
	}
	var capturedSlug string
	fetch := func(_ context.Context, slug string) (*circleci.ProjectDetail, error) {
		capturedSlug = slug
		return &circleci.ProjectDetail{ID: "proj-1", OrgID: "org-1", Slug: slug}, nil
	}

	prompts := fakePrompts(
		[]int{0},
		[]string{"dev", "550e8400-e29b-41d4-a716-446655440000", "", "", ""},
		[]bool{false},
		nil,
	)

	_, err := CollectRunConfig(context.Background(), prompts, projects, nil, fetch, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedSlug != "bb/acme/api" {
		t.Fatalf("slug = %q, want %q", capturedSlug, "bb/acme/api")
	}
}
