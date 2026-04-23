package task

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

// Prompts provides the interactive UI operations needed to collect run config.
type Prompts struct {
	Confirm    func(label string, defaultVal bool) (bool, error)
	SelectFrom func(label string, items []string) (int, error)
	PromptText func(label string, defaultVal string) (string, error)
	Warn       func(msg string)
}

// ProjectDetailFunc fetches project detail by slug (e.g. "gh/org/repo").
type ProjectDetailFunc func(ctx context.Context, slug string) (*circleci.ProjectDetail, error)

// CollectRunConfig drives the interactive form to build a RunConfig.
// It takes already-fetched projects and collaborations plus injected UI
// and data-fetch dependencies so the logic is testable without a TTY.
// envOrgID is the value of CIRCLECI_ORG_ID from the environment (pass "" to skip the mismatch check).
func CollectRunConfig(
	ctx context.Context,
	prompts Prompts,
	projects []circleci.FollowedProject,
	collabs []circleci.Collaboration,
	fetchDetail ProjectDetailFunc,
	envOrgID string,
) (*RunConfig, error) {
	orgID, projectID, orgType, err := collectProject(ctx, prompts, projects, collabs, fetchDetail)
	if err != nil {
		return nil, err
	}

	if envOrgID != "" && envOrgID != orgID {
		prompts.Warn(fmt.Sprintf(
			"Warning: selected project org (%s) differs from %s (%s)",
			orgID, "CIRCLECI_ORG_ID", envOrgID,
		))
	}

	definitions, err := collectDefinitions(prompts)
	if err != nil {
		return nil, err
	}

	return &RunConfig{
		OrgID:       orgID,
		ProjectID:   projectID,
		OrgType:     orgType,
		Definitions: definitions,
	}, nil
}

func collectProject(
	ctx context.Context,
	prompts Prompts,
	projects []circleci.FollowedProject,
	collabs []circleci.Collaboration,
	fetchDetail ProjectDetailFunc,
) (orgID, projectID, orgType string, err error) {
	// Sort projects alphabetically
	sort.Slice(projects, func(i, j int) bool {
		a := fmt.Sprintf("%s/%s", projects[i].Username, projects[i].Reponame)
		b := fmt.Sprintf("%s/%s", projects[j].Username, projects[j].Reponame)
		return a < b
	})

	// Build project selection list
	items := make([]string, 0, len(projects)+1)
	for _, p := range projects {
		items = append(items, fmt.Sprintf("%s/%s", p.Username, p.Reponame))
	}
	items = append(items, "Enter manually")

	idx, err := prompts.SelectFrom("Select a project:", items)
	if err != nil {
		return "", "", "", err
	}

	if idx < len(projects) {
		return resolveProjectFromList(ctx, projects[idx], fetchDetail)
	}
	return collectManualProject(prompts, collabs)
}

func resolveProjectFromList(
	ctx context.Context,
	p circleci.FollowedProject,
	fetchDetail ProjectDetailFunc,
) (orgID, projectID, orgType string, err error) {
	vcsPrefix := "gh"
	if strings.EqualFold(p.VcsType, "bitbucket") {
		vcsPrefix = "bb"
	}
	slug := fmt.Sprintf("%s/%s/%s", vcsPrefix, p.Username, p.Reponame)

	detail, err := fetchDetail(ctx, slug)
	if err != nil {
		return "", "", "", fmt.Errorf("fetch project details: %w", err)
	}
	return detail.OrgID, detail.ID, MapVcsTypeToOrgType(p.VcsType), nil
}

func collectManualProject(
	prompts Prompts,
	collabs []circleci.Collaboration,
) (orgID, projectID, orgType string, err error) {
	if len(collabs) == 0 {
		return "", "", "", fmt.Errorf("no organizations found")
	}

	orgItems := make([]string, len(collabs))
	for i, c := range collabs {
		orgItems[i] = c.Name
	}

	orgIdx, err := prompts.SelectFrom("Select your organization:", orgItems)
	if err != nil {
		return "", "", "", err
	}

	orgID = collabs[orgIdx].ID
	orgType = MapVcsTypeToOrgType(collabs[orgIdx].VcsType)

	projectID, err = prompts.PromptText("Project ID (UUID)", "")
	if err != nil {
		return "", "", "", err
	}
	if projectID == "" {
		return "", "", "", fmt.Errorf("project ID is required")
	}
	return orgID, projectID, orgType, nil
}

func collectDefinitions(prompts Prompts) (map[string]RunDefinition, error) {
	definitions := make(map[string]RunDefinition)

	for {
		name, err := prompts.PromptText("Definition name (e.g. dev, prod)", "")
		if err != nil {
			return nil, err
		}
		if name == "" {
			return nil, fmt.Errorf("definition name is required")
		}

		defID, err := collectRequiredUUID(prompts, "Definition ID (UUID)")
		if err != nil {
			return nil, err
		}

		description, err := prompts.PromptText("Description (optional)", "")
		if err != nil {
			return nil, err
		}

		defaultBranch, err := prompts.PromptText("Default branch", "main")
		if err != nil {
			return nil, err
		}

		envID, err := collectOptionalUUID(prompts, "Environment ID (optional UUID)")
		if err != nil {
			return nil, err
		}

		def := RunDefinition{
			DefinitionID:  defID,
			Description:   description,
			DefaultBranch: defaultBranch,
		}
		if envID != "" {
			def.ChunkEnvironmentID = &envID
		}

		definitions[name] = def

		more, err := prompts.Confirm("Add another definition?", false)
		if err != nil || !more {
			break
		}
	}

	return definitions, nil
}

func collectRequiredUUID(prompts Prompts, label string) (string, error) {
	for {
		val, err := prompts.PromptText(label, "")
		if err != nil {
			return "", err
		}
		if val == "" {
			prompts.Warn("  This field is required.")
			continue
		}
		if !IsValidUUID(val) {
			prompts.Warn("  Must be a valid UUID (e.g. 550e8400-e29b-41d4-a716-446655440000).")
			continue
		}
		return val, nil
	}
}

func collectOptionalUUID(prompts Prompts, label string) (string, error) {
	for {
		val, err := prompts.PromptText(label, "")
		if err != nil {
			return "", err
		}
		if val == "" {
			return "", nil
		}
		if !IsValidUUID(val) {
			prompts.Warn("  Must be a valid UUID (e.g. 550e8400-e29b-41d4-a716-446655440000).")
			continue
		}
		return val, nil
	}
}
