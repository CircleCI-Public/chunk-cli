import type { CircleCIProject } from "../api/circleci";

const UUID_REGEX = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isValidUuid(value: string): boolean {
	return UUID_REGEX.test(value);
}

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
