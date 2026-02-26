#!/usr/bin/env bun
import packageJson from "../package.json";
import { runAuth } from "./commands/auth";
import { runBuildPrompt } from "./commands/build-prompt";
import { runConfig } from "./commands/config";
import { runUpgrade } from "./commands/upgrade";
import { runVersion } from "./commands/version";
import type { CommandResult } from "./types";
import { parseArgs } from "./utils/args";
import { isAuthError, isNetworkError, printError } from "./utils/errors";

function showMainHelp(): void {
	console.log(`chunk ${packageJson.version}

Context generation CLI — mines real reviewer patterns to build AI agent prompts

Usage: chunk [command] [options]

Commands:
  help           Show help (default)
  auth           Manage authentication
  config         Manage configuration
  upgrade        Update to latest version
  version        Show version information
  build-prompt   Discover top reviewers and generate a PR review agent prompt

Options:
  -h, --help       Show help for a command
  -v, --version    Show version number

Examples:
  chunk                      Show help
  chunk auth login           Configure API key

Run 'chunk' for more information on a command.
`);
}

async function main(): Promise<void> {
	const parsed = parseArgs(process.argv);
	let result: CommandResult;

	if (parsed.flags.version) {
		result = await runVersion();
		process.exit(result.exitCode);
	}

	switch (parsed.command) {
		case "help":
			showMainHelp();
			result = { exitCode: 0 };
			break;
		case "auth":
			result = await runAuth(parsed);
			break;
		case "config":
			result = await runConfig(parsed);
			break;
		case "upgrade":
			result = await runUpgrade(parsed);
			break;
		case "version":
			result = await runVersion();
			break;
		case "build-prompt":
			result = await runBuildPrompt(parsed);
			break;
		default:
			if (parsed.flags.help) {
				showMainHelp();
				result = { exitCode: 0 };
			} else {
				printError(
					`Unknown command: ${parsed.command}`,
					"The command you entered is not recognized.",
					"Run `chunk --help` to see available commands.",
				);
				result = { exitCode: 2 };
			}
	}

	process.exit(result.exitCode);
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
