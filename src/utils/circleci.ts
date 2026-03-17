import type { CircleCIProject } from "../api/circleci";

export function mapVcsTypeToOrgType(vcsType: string | undefined): "github" | "circleci" {
	const lower = vcsType?.toLowerCase();
	if (lower === "github" || lower === "gh") return "github";
	return "circleci";
}

export function buildProjectSlug(vcsType: string | undefined, org: string, repo: string): string {
	const prefix = vcsType?.toLowerCase() === "bitbucket" ? "bb" : "gh";
	return `${prefix}/${org}/${repo}`;
}

export function sortProjectsByName(projects: CircleCIProject[]): CircleCIProject[] {
	return [...projects].sort((a, b) =>
		`${a.username}/${a.reponame}`.localeCompare(`${b.username}/${b.reponame}`),
	);
}
