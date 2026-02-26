#!/usr/bin/env bun
import { Command } from "@commander-js/extra-typings";
import packageJson from "../package.json";
import { runAuthLogin, runAuthLogout, runAuthStatus } from "./commands/auth";
import {
	DEFAULT_ANALYZE_MODEL,
	DEFAULT_PROMPT_MODEL,
	type ParsedBuildPromptFlags,
	runBuildPrompt,
} from "./commands/build-prompt";
import { runConfigSet, runConfigShow } from "./commands/config";
import { runUpgrade } from "./commands/upgrade";
import { runVersion } from "./commands/version";
import { isAuthError, isNetworkError, printError } from "./utils/errors";

const program = new Command();
program
	.name("chunk")
	.version(packageJson.version as string)
	.description("AI code review CLI")
	.helpOption("-h, --help");

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

async function main(): Promise<void> {
	program
		.command("build-prompt")
		.addHelpText("after", `
Environment Variables:
  GITHUB_TOKEN             Required: GitHub personal access token with repo scope
  ANTHROPIC_API_KEY        Required: Anthropic API key

  -h, --help               Show this help message

Examples:
  chunk build-prompt --org myorg --repos myrepo
  chunk build-prompt --org myorg --repos repo1,repo2 --top 5 --output ./my-prompt.md`)
		.requiredOption("--org <org>", "GitHub organization to analyze")
		.option("--repos <items>", "Comma-separated list of repo names", parseCommaSeparatedList, [])
		.option("--top <number>", "Number of top reviewers to analyze", parsePositiveInt, 5)
		.option("--since <date>", "Start date YYYY-MM-DD", parseDate, threeMonthsAgo())
		.option("--output <path>", "Output path for the generated prompt", "./pr-review-prompt.md")
		.option("--max-comments <number>", "Max comments per reviewer for analysis", parsePositiveInt)
		.option("--analyze-model <model>", "Claude model for the analysis step", DEFAULT_ANALYZE_MODEL)
		.option("--prompt-model <model>", "Claude model for prompt generation", DEFAULT_PROMPT_MODEL)
		.option("--include-attribution", "Include reviewer attribution in the generated prompt", false)
		.action(async (options: ParsedBuildPromptFlags) => {
			process.exit((await runBuildPrompt(options)).exitCode);
		});

	const auth = program.command("auth").description("Manage authentication");
	auth
		.command("login")
		.description("Store API key for authentication")
		.action(async () => process.exit((await runAuthLogin()).exitCode));
	auth
		.command("status")
		.description("Check authentication status")
		.action(async () => process.exit((await runAuthStatus()).exitCode));
	auth
		.command("logout")
		.description("Remove stored credentials")
		.action(async () => process.exit((await runAuthLogout()).exitCode));

	const config = program.command("config").description("Manage configuration");
	config
		.command("show")
		.description("Display current configuration")
		.action(() => process.exit(runConfigShow().exitCode));
	config
		.command("set")
		.addHelpText("after", `
Examples:
  chunk config set model opus`)
		.description("Set a configuration value")
		.argument("<key>", "config key (model, apiKey)")
		.argument("<value>", "value to set")
		.action((key: string, value: string) => process.exit(runConfigSet(key, value).exitCode));

	program
		.command("upgrade")
		.description("Update to the latest version")
		.action(async () => process.exit((await runUpgrade()).exitCode));

	program.command("version").action(async () => process.exit((await runVersion()).exitCode));

	program.action(() => {
		program.outputHelp();
		process.exit(0);
	});

	await program.parseAsync(process.argv);
}

main().catch((error) => {
	const err = error instanceof Error ? error : new Error(String(error));
	let suggestion: string;
	if (isNetworkError(err)) {
		suggestion = "Check your internet connection and try again.";
	} else if (isAuthError(err)) {
		suggestion = "Run `chunk auth login` to set up your API key.";
	} else {
		suggestion = "If this problem persists, please report an issue.";
	}
	printError(err.message, undefined, suggestion);
	process.exit(2);
});
