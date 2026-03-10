import { extractCommentsAndBuildPrompt, resolveOrgAndRepos } from "../core/build-prompt";
import type { CommandResult } from "../types";

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
	const { org, repos } = await resolveOrgAndRepos({
		org: flags.org,
		repos: flags.repos,
	});

	await extractCommentsAndBuildPrompt({
		org,
		repos,
		top: flags.top,
		since: flags.since,
		outputPath: flags.output,
		maxComments: flags.maxComments,
		analyzeModel: flags.analyzeModel,
		promptModel: flags.promptModel,
		includeAttribution: flags.includeAttribution,
	});

	return { exitCode: 0 };
}
