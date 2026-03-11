import { detectGitHubOrgAndRepo } from "../utils/git-remote";

/**
 * Resolve org and repos from flags, auto-detecting from git remote when needed.
 *
 * Behavior matrix:
 *   no flags              → auto-detect org and current repo from git remote
 *   --repos only          → auto-detect org from git remote, use provided repos
 *   --org + --repos       → use provided org and repos
 *   --org only (no repos) → error: no way to enumerate all repos in an org
 */
export async function resolveOrgAndRepos(flags: {
	org?: string;
	repos: string[];
}): Promise<{ org: string; repos: string[] }> {
	if (flags.org && flags.repos.length === 0) {
		throw new Error(
			"--repos is required when --org is provided. Scanning all repos in an org is not supported.\n" +
				"  Omit --org to auto-detect from git remote, or specify repos with --repos.",
		);
	}

	if (flags.org) {
		return { org: flags.org, repos: flags.repos };
	}

	// Auto-detect org (and optionally repo) from git remote
	const detected = await detectGitHubOrgAndRepo();
	return {
		org: detected.org,
		repos: flags.repos.length > 0 ? flags.repos : [detected.repo],
	};
}

export interface BuildPromptOptions {
	org: string;
	repos: string[];
	top: number;
	since: Date;
	outputPath: string;
	maxComments?: number;
	analyzeModel: string;
	promptModel: string;
	includeAttribution: boolean;
}
