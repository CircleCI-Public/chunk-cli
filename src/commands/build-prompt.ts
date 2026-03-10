import { existsSync } from "node:fs";
import { resolve } from "node:path";
import { DEFAULT_OUTPUT_PATH, LEGACY_OUTPUT_PATH } from "../config";
import { extractCommentsAndBuildPrompt, resolveOrgAndRepos } from "../core/build-prompt";
import type { CommandResult } from "../types";
import { yellow } from "../ui/colors";

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
	// Warn if a legacy output file exists and the user is using the new default path
	if (
		resolve(flags.output) === resolve(DEFAULT_OUTPUT_PATH) &&
		existsSync(resolve(LEGACY_OUTPUT_PATH))
	) {
		console.log(
			yellow(
				`[deprecation] Found ${LEGACY_OUTPUT_PATH} in the working directory.\n` +
					`  The default output path has changed to ${DEFAULT_OUTPUT_PATH}.\n` +
					`  Update scripts that reference the old path, or pass --output ${LEGACY_OUTPUT_PATH} to keep the old behavior.`,
			),
		);
		console.log("");
	}

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
