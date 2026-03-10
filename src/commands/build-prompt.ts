import type { Command } from "@commander-js/extra-typings";
import { DEFAULT_ANALYZE_MODEL, DEFAULT_PROMPT_MODEL } from "../config";
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
  chunk build-prompt --repos api,backend                 # Auto-detect org, analyze specific repos
  chunk build-prompt --org myorg --repos api,backend     # Explicit org and repos
  chunk build-prompt --top 10 --since 2025-01-01         # More reviewers, custom date range

Note: --org without --repos is an error (cannot enumerate all repos in an org).
      Omit --org to auto-detect from git remote.`,
		)
		.option(
			"--org <org>",
			"GitHub organization to analyze (auto-detected from git remote if omitted)",
		)
		.option("--repos <items>", "Comma-separated list of repo names", parseCommaSeparatedList, [])
		.option("--top <number>", "Number of top reviewers to analyze", parsePositiveInt, 5)
		.option("--since <date>", "Start date YYYY-MM-DD", parseDate, threeMonthsAgo())
		.option("--output <path>", "Output path for the generated prompt", "./review-prompt.md")
		.option("--max-comments <number>", "Max comments per reviewer for analysis", parsePositiveInt)
		.option("--analyze-model <model>", "Claude model for the analysis step", DEFAULT_ANALYZE_MODEL)
		.option("--prompt-model <model>", "Claude model for prompt generation", DEFAULT_PROMPT_MODEL)
		.option("--include-attribution", "Include reviewer attribution in the generated prompt", false)
		.action(async (options: ParsedBuildPromptFlags) => {
			process.exit((await runBuildPrompt(options)).exitCode);
		});
}

async function runBuildPrompt(flags: ParsedBuildPromptFlags): Promise<CommandResult> {
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
