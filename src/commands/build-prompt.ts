import { type BuildPromptOptions, extractCommentsAndBuildPrompt } from "../core/build-prompt";
import { resolveOrgAndRepos } from "../core/build-prompt.steps";
import type { CommandResult } from "../types";
import { dim } from "../ui/colors";

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
	const resolved = await resolveOrgAndRepos({ org: flags.org, repos: flags.repos });

	if (!flags.org) {
		console.log(dim(`Detected org from git remote: ${resolved.org}`));
	}

	const options: BuildPromptOptions = {
		org: resolved.org,
		repos: resolved.repos,
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
