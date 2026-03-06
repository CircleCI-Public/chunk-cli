#!/usr/bin/env bun
import { registerHookCommands } from "@chunk/hook";
import { Command } from "@commander-js/extra-typings";
import { runAuthLogin, runAuthLogout, runAuthStatus } from "./commands/auth";
import { type ParsedBuildPromptFlags, runBuildPrompt } from "./commands/build-prompt";
import { runConfigSet, runConfigShow } from "./commands/config";
import {
	addSshKeyToSandbox,
	createNewSandbox,
	execCommandInSandbox,
	listSandboxes,
} from "./commands/sandbox";
import { runSandboxPrepare } from "./commands/sandbox/prepare";
import { runSkillsInstall, runSkillsList, runSkillsStatus } from "./commands/skills";
import { runTaskConfig, runTaskRun } from "./commands/task";
import { runUpgrade } from "./commands/upgrade";
import { DEFAULT_ANALYZE_MODEL, DEFAULT_PROMPT_MODEL } from "./config";
import { isAuthError, isNetworkError, printError } from "./utils/errors";

const program = new Command();
program.name("chunk").version(VERSION).description("AI code review CLI").helpOption("-h, --help");

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
		.addHelpText(
			"after",
			`
Environment Variables:
  GITHUB_TOKEN             Required: GitHub personal access token with repo scope
  ANTHROPIC_API_KEY        Required: Anthropic API key

  -h, --help               Show this help message

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
		.option("--output <path>", "Output path for the generated prompt", "./review-prompt.md")
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
		.addHelpText(
			"after",
			`
Examples:
  chunk config set model opus`,
		)
		.description("Set a configuration value")
		.argument("<key>", "config key (model, apiKey)")
		.argument("<value>", "value to set")
		.action((key: string, value: string) => process.exit(runConfigSet(key, value).exitCode));

	const skills = program.command("skills").description("Install and manage AI agent skills");
	skills
		.command("install")
		.description("Install or update all skills into agent config directories")
		.action(() => process.exit(runSkillsInstall().exitCode));
	skills
		.command("status")
		.description("Show current installation status without making changes")
		.action(() => process.exit(runSkillsStatus().exitCode));
	skills
		.command("list")
		.description("List skills bundled in this binary")
		.action(() => process.exit(runSkillsList().exitCode));

	const task = program.command("task").description("Trigger and configure chunk pipeline runs");

	task
		.command("run")
		.description("Trigger a chunk run against a CircleCI pipeline definition")
		.addHelpText(
			"after",
			`
Environment Variables:
  CIRCLE_TOKEN             Required: CircleCI personal API token

Examples:
  chunk task run --definition dev --prompt "Fix the flaky test in auth.spec.ts"
  chunk task run --definition prod --prompt "Refactor the payment module" --branch main --new-branch
  chunk task run --definition dev --prompt "Add type annotations" --no-pipeline-as-tool
  chunk task run --definition 550e8400-e29b-41d4-a716-446655440000 --prompt "Fix the flaky test"`,
		)
		.requiredOption(
			"--definition <name>",
			"Definition name from .chunk/run.json, or a definition UUID",
		)
		.requiredOption("--prompt <text>", "Prompt to send to the agent")
		.option("--branch <branch>", "Branch to check out (overrides definition default)")
		.option("--new-branch", "Create a new branch for the run", false)
		.option("--pipeline-as-tool", "Run the pipeline as a tool call", true)
		.action(async (options) => {
			process.exit((await runTaskRun(options)).exitCode);
		});

	task
		.command("config")
		.description("Initialize .chunk/run.json for this repository")
		.addHelpText(
			"after",
			`
Environment Variables:
  CIRCLE_TOKEN             Required: CircleCI personal API token`,
		)
		.action(async () => process.exit((await runTaskConfig()).exitCode));

	program
		.command("upgrade")
		.description("Update to the latest version")
		.action(async () => process.exit((await runUpgrade()).exitCode));

	const sandbox = program.command("sandbox").description("Manage sandbox environments for testing");
	sandbox
		.command("prepare")
		.description("Prepare the hook environment before a session begins")
		.action(async () => process.exit((await runSandboxPrepare()).exitCode));

	// Hook commands — exec, task, sync, state, scope for AI agent hooks
	const hook = program
		.command("hook")
		.description("Manage AI coding agent hooks (exec, task, sync, state, scope)");
	registerHookCommands(hook);

	const sandboxes = program.command("sandboxes").description("Manage sandboxes");
	sandboxes
		.command("list")
		.description("List all sandboxes in an organization")
		.requiredOption("--org-id <orgId>", "Org ID to list sandboxes for")
		.action(async (options) => process.exit((await listSandboxes(options.orgId)).exitCode));

	sandboxes
		.command("create")
		.description("Create a new sandbox")
		.requiredOption("--org-id <orgId>", "Organization ID")
		.requiredOption("--name <name>", "Sandbox name")
		.option("--image <image>", "Sandbox image")
		.action(async (options) =>
			process.exit((await createNewSandbox(options.orgId, options.name, options.image)).exitCode),
		);

	sandboxes
		.command("add-ssh-key")
		.description("Add an SSH public key to a sandbox")
		.requiredOption("--org-id <orgId>", "Organization ID")
		.requiredOption("--sandbox-id <sandboxId>", "Sandbox ID")
		.requiredOption("--public-key <publicKey>", "SSH public key to add")
		.action(async (options) =>
			process.exit(
				(await addSshKeyToSandbox(options.orgId, options.sandboxId, options.publicKey)).exitCode,
			),
		);

	sandboxes
		.command("exec")
		.description("Execute a command in a sandbox")
		.requiredOption("--org-id <orgId>", "Org ID of sandbox")
		.requiredOption("--sandbox-id <sandboxId>", "Sandbox ID of sandbox")
		.requiredOption("--command <command>", "Command to execute")
		.option("--args <args...>", "Arguments to command", [])
		.action(async (options) =>
			process.exit(
				(
					await execCommandInSandbox(
						options.orgId,
						options.sandboxId,
						options.command,
						options.args,
					)
				).exitCode,
			),
		);

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
