import { detectGitHubOrgAndRepo } from "../utils/git-remote";

/**
 * Resolve org and repos from explicit flags or git remote auto-detection.
 * Pure decision logic — no UI output.
 */
export async function resolveOrgAndRepos(flags: {
	org?: string;
	repos: string[];
}): Promise<{ org: string; repos: string[] }> {
	const { org, repos } = flags;

	if (org && repos.length === 0) {
		throw new Error(
			"--repos is required when --org is provided. Omit --org to auto-detect from git remote.",
		);
	}

	if (org) {
		return { org, repos };
	}

	const detected = await detectGitHubOrgAndRepo();
	return {
		org: detected.org,
		repos: repos.length > 0 ? repos : [detected.repo],
	};
}

/**
 * Derive the intermediate output file paths from the main output path.
 */
export function deriveOutputPaths(outputPath: string): {
	outputBase: string;
	detailsPath: string;
	analysisPath: string;
} {
	const outputBase = outputPath.replace(/\.md$/, "");
	return {
		outputBase,
		detailsPath: `${outputBase}-details.json`,
		analysisPath: `${outputBase}-analysis.md`,
	};
}
