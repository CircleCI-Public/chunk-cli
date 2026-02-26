import { type BuildPromptOptions, extractCommentsAndBuildPrompt } from "../core/build-prompt";
import type { CommandResult } from "../types";

export const DEFAULT_ANALYZE_MODEL = "claude-sonnet-4-5-20250929";
export const DEFAULT_PROMPT_MODEL = "claude-opus-4-5-20251101";

export interface ParsedBuildPromptFlags {
	org: string;
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
	const options: BuildPromptOptions = {
		org: flags.org,
		repos: flags.repos,
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
