import { type BuildPromptOptions, extractCommentsAndBuildPrompt } from "../core/build-prompt";
import type { CommandResult } from "../types";
import { dim } from "../ui/colors";
import { detectGitHubOrgAndRepo } from "../utils/git-remote";

export interface ParsedBuildPromptFlags {
	org?: string;
	repos: string[];
	top: number;
	since: Date;
	output: string;
	maxComments?: number;
	analyzeModel: string;
	promptModel: string;
	includeAttribution: boolean;
}

export async function runBuildPrompt(flags: ParsedBuildPromptFlags): Promise<CommandResult> {
	let org = flags.org;
	let repos = [...flags.repos];

	if (org && repos.length === 0) {
		throw new Error(
			"--repos is required when --org is provided. There is no way to enumerate all repos in an org.\n" +
				"  Omit --org to auto-detect from git remote, or specify repos with --repos.",
		);
	}

	if (!org) {
		const detected = await detectGitHubOrgAndRepo();
		org = detected.org;
		if (repos.length === 0) {
			repos = [detected.repo];
		}
		console.log(dim(`Detected org from git remote: ${detected.org}`));
	}

	const options: BuildPromptOptions = {
		org,
		repos,
		top: flags.top,
		since: flags.since,
		outputPath: flags.output,
		maxComments: flags.maxComments,
		analyzeModel: flags.analyzeModel,
		promptModel: flags.promptModel,
		includeAttribution: flags.includeAttribution,
	};

	await extractCommentsAndBuildPrompt(options);
	return { exitCode: 0 };
}
