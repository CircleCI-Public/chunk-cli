import { existsSync } from "node:fs";
import { resolve } from "node:path";
import type { Command } from "@commander-js/extra-typings";
import {
	DEFAULT_ANALYZE_MODEL,
	DEFAULT_OUTPUT_PATH,
	DEFAULT_PROMPT_MODEL,
	LEGACY_OUTPUT_PATH,
} from "../config";
import { type BuildPromptOptions, extractCommentsAndBuildPrompt } from "../core/build-prompt";
import { resolveOrgAndRepos } from "../core/build-prompt.steps";
import type { CommandResult } from "../types";
import { dim, yellow } from "../ui/colors";

interface ParsedBuildPromptFlags {
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

function parsePositiveInt(value: string, _dummyPrevious: unknown): number {
	const n = parseInt(value, 10);
	if (Number.isNaN(n) || n < 1) {
		throw new Error("Not a number.");
	}
	return n;
}

function parseCommaSeparatedList(value: string, _dummyPrevious: unknown): string[] {
	return value.split(",");
}

function parseDate(value: string, _dummyPrevious: unknown): Date {
	const date = new Date(value);
	if (Number.isNaN(date.getTime())) {
		throw new Error(`Invalid date: ${value}`);
	}
	return date;
}

function threeMonthsAgo(): Date {
	const date = new Date();
	date.setMonth(date.getMonth() - 3);
	return date;
}

export function registerBuildPromptCommand(program: Command): void {
	program
		.command("build-prompt")
		.addHelpText(
			"after",
			`
Environment Variables:
  GITHUB_TOKEN             Required: GitHub personal access token with repo scope
  ANTHROPIC_API_KEY        Required: Anthropic API key

Examples:
  chunk build-prompt                                     # Auto-detect org and repo from git remote
  chunk build-prompt --org myorg --repos myrepo          # Explicit org and repo(s)
  chunk build-prompt --repos repo1,repo2                 # Auto-detect org, explicit repos`,
		)
		.option(
			"--org <org>",
			"GitHub organization to analyze (auto-detected from git remote if omitted)",
		)
		.option("--repos <items>", "Comma-separated list of repo names", parseCommaSeparatedList, [])
		.option("--top <number>", "Number of top reviewers to analyze", parsePositiveInt, 5)
		.option("--since <date>", "Start date YYYY-MM-DD", parseDate, threeMonthsAgo())
		.option("--output <path>", "Output path for the generated prompt", DEFAULT_OUTPUT_PATH)
		.option("--max-comments <number>", "Max comments per reviewer for analysis", parsePositiveInt)
		.option("--analyze-model <model>", "Claude model for the analysis step", DEFAULT_ANALYZE_MODEL)
		.option("--prompt-model <model>", "Claude model for prompt generation", DEFAULT_PROMPT_MODEL)
		.option("--include-attribution", "Include reviewer attribution in the generated prompt", false)
		.action(async (options) => {
			process.exit((await runBuildPrompt(options)).exitCode);
		});
}

async function runBuildPrompt(flags: ParsedBuildPromptFlags): Promise<CommandResult> {
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
