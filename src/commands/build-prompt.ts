import { resolve } from "node:path";
import { type BuildPromptOptions, extractCommentsAndBuildPrompt } from "../core/build-prompt";
import type { CommandResult, ParsedArgs } from "../types";
import { printError } from "../utils/errors";

const DEFAULT_ANALYZE_MODEL = "claude-sonnet-4-5-20250929";
const DEFAULT_PROMPT_MODEL = "claude-opus-4-5-20251101";

function threeMonthsAgo(): string {
	const date = new Date();
	date.setMonth(date.getMonth() - 3);
	return date.toISOString().slice(0, 10);
}

function parseDate(value: string): Date {
	const date = new Date(value);
	if (Number.isNaN(date.getTime())) {
		throw new Error(`Invalid date: ${value}`);
	}
	return date;
}

export function showBuildPromptHelp(): void {
	console.log(`
Usage: chunk build-prompt --org <org> [options]

Run the full pipeline: discover top reviewers, analyze their review patterns,
and generate a PR review agent prompt.

Required:
  --org <org>              GitHub organization to analyze

Options:
  --repos <repos>          Comma-separated list of repo names (default: all in org)
  --top <n>                Number of top reviewers to analyze (default: 5)
  --since <date>           Start date YYYY-MM-DD (default: 3 months ago)
  --output <path>          Output path for the generated prompt (default: ./pr-review-prompt.md)
  --max-comments <n>       Max comments per reviewer for analysis (default: all)
  --analyze-model <model>  Claude model for the analysis step (default: ${DEFAULT_ANALYZE_MODEL})
  --prompt-model <model>   Claude model for the prompt generation step (default: ${DEFAULT_PROMPT_MODEL})
  --include-attribution    Include reviewer attribution in the generated prompt

Environment Variables:
  GITHUB_TOKEN             Required: GitHub personal access token with repo scope
  ANTHROPIC_API_KEY        Required: Anthropic API key

  -h, --help               Show this help message

Examples:
  chunk build-prompt --org myorg --repos myrepo
  chunk build-prompt --org myorg --repos repo1,repo2 --top 5 --output ./my-prompt.md
`);
}

export async function runBuildPrompt(parsed: ParsedArgs): Promise<CommandResult> {
	if (parsed.flags.help) {
		showBuildPromptHelp();
		return { exitCode: 0 };
	}

	const org = parsed.flags.org;
	if (!org || typeof org !== "string") {
		printError(
			"Missing required flag: --org",
			"Specify the GitHub organization to analyze.",
			"Run `chunk build-prompt --help` to see all options.",
		);
		return { exitCode: 1 };
	}

	const sinceStr = typeof parsed.flags.since === "string" ? parsed.flags.since : threeMonthsAgo();
	let since: Date;
	try {
		since = parseDate(sinceStr);
	} catch {
		printError(`Invalid date: ${sinceStr}`, "Use YYYY-MM-DD format (e.g. 2025-01-01).");
		return { exitCode: 1 };
	}

	const outputRaw =
		typeof parsed.flags.output === "string" ? parsed.flags.output : "./pr-review-prompt.md";
	const outputPath = resolve(process.cwd(), outputRaw);

	const repos =
		typeof parsed.flags.repos === "string"
			? parsed.flags.repos.split(",").map((r) => r.trim())
			: [];

	const top = typeof parsed.flags.top === "string" ? parseInt(parsed.flags.top, 10) : 5;

	const maxComments =
		typeof parsed.flags["max-comments"] === "string"
			? parseInt(parsed.flags["max-comments"], 10)
			: undefined;

	const analyzeModel =
		typeof parsed.flags["analyze-model"] === "string"
			? parsed.flags["analyze-model"]
			: DEFAULT_ANALYZE_MODEL;

	const promptModel =
		typeof parsed.flags["prompt-model"] === "string"
			? parsed.flags["prompt-model"]
			: DEFAULT_PROMPT_MODEL;

	const includeAttribution = parsed.flags["include-attribution"] === true;

	const options: BuildPromptOptions = {
		org,
		repos,
		top,
		since,
		outputPath,
		maxComments,
		analyzeModel,
		promptModel,
		includeAttribution,
	};

	await extractCommentsAndBuildPrompt(options);
	return { exitCode: 0 };
}
