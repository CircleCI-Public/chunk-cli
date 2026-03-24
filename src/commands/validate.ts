/**
 * `chunk validate` — run the full validation suite.
 * `chunk validate:<name>` — run a single named command with caching.
 * `chunk validate:init` — auto-detect and set up commands.
 *
 * Colon syntax is rewritten to positional args in `src/index.ts`
 * before Commander parses, so `validate:<name>` becomes `validate <name>`.
 */

import { PROFILES } from "@chunk/hook";
import type { Command } from "@commander-js/extra-typings";
import { executeCommand, listCommands, resolveRunCommand, saveCommand } from "../core/run";
import { DEFAULT_TIMEOUT } from "../core/run-config";
import type { RunResult } from "../core/run-executor";
import type { ValidateMode, ValidateStepResult } from "../core/validate";
import { runValidate } from "../core/validate";
import type { CommandResult } from "../types/index";
import { bold, dim, green, red, yellow } from "../ui/colors";
import { formatCommandList } from "../ui/format";
import { promptConfirm, promptInput } from "../ui/prompt";
import { printError } from "../utils/errors";
import { requireToken } from "../utils/tokens";
import { runValidateInit } from "./validate/init";

const NO_COMMANDS_HINT = "Run `chunk validate:init` to detect your install and test commands.";

function renderValidateResult(results: ValidateStepResult[], skipped: string[]): CommandResult {
	const passed = results.every((r) => r.exitCode === 0);

	process.stdout.write(`\n${bold("─".repeat(40))}\n`);
	for (const { command, exitCode } of results) {
		const icon = exitCode === 0 ? green("✓") : red("✗");
		process.stdout.write(`${icon} ${command}\n`);
	}
	for (const command of skipped) {
		process.stdout.write(`${dim("○")} ${yellow(command)} ${dim("(skipped)")}\n`);
	}

	return { exitCode: passed ? 0 : 1 };
}

function renderExecution(name: string, result: RunResult): void {
	if (result.status === "cached") {
		console.log(
			`${green("✓")} ${name}: cached result (${result.exitCode === 0 ? "pass" : "fail"})`,
		);
	} else if (result.status === "pass") {
		console.log(`${green("✓")} ${name}: pass`);
	} else {
		console.error(`${red("✗")} ${name}: fail (exit ${result.exitCode})`);
	}

	if (result.output) {
		const stream = result.exitCode !== 0 ? process.stderr : process.stdout;
		stream.write(result.output);
		if (!result.output.endsWith("\n")) stream.write("\n");
	}
}

function handleValidateError(error: string, hint?: string): CommandResult {
	const resolvedHint =
		hint ?? (error === "No validate commands configured" ? NO_COMMANDS_HINT : undefined);
	printError(error, resolvedHint);
	return { exitCode: 1 };
}

function renderList(projectDir: string): void {
	const commands = listCommands(projectDir);

	if (commands.length === 0) {
		console.log(`No commands configured.\n`);
		console.log(`Add commands to ${bold(".chunk/config.json")}:\n`);
		console.log(`  ${dim('chunk validate:test --cmd "npm test" --save')}`);
		console.log(`  ${dim('chunk validate:lint --cmd "npm run lint" --save')}`);
		return;
	}

	console.log(formatCommandList(commands));
}

async function handleSingleCommand(
	projectDir: string,
	name: string,
	opts: { cmd?: string; save?: boolean; force?: boolean; status?: boolean },
): Promise<number> {
	const resolved = resolveRunCommand(projectDir, name, opts, process.stdin.isTTY === true);

	switch (resolved.type) {
		case "status-cached": {
			console.log(`${green("✓")} ${name}: cached (${resolved.exitCode === 0 ? "pass" : "fail"})`);
			return resolved.exitCode;
		}
		case "status-miss": {
			console.log(`${dim("○")} ${name}: no cached result`);
			return 0;
		}
		case "not-configured": {
			printError(
				`Command "${name}" is not configured`,
				undefined,
				`Add "${name}" to .chunk/config.json`,
			);
			return 1;
		}
		case "needs-setup": {
			console.log(`Command ${bold(name)} is not configured yet.\n`);
			const input = await promptInput(`What command should ${bold(name)} run? `);
			const trimmed = input.trim();
			if (!trimmed) {
				console.log(dim("No command entered, aborting."));
				return 1;
			}
			saveCommand(projectDir, name, trimmed);
			console.log(`${green("✓")} Saved ${bold(name)} to .chunk/config.json\n`);
			const result = executeCommand(projectDir, name, trimmed, {
				force: opts.force,
				timeout: DEFAULT_TIMEOUT,
			});
			renderExecution(name, result);
			return result.exitCode !== 0 ? 1 : 0;
		}
		case "executed": {
			if (resolved.saveAction === "save") {
				saveCommand(projectDir, name, opts.cmd as string);
				console.log(`${green("✓")} Saved ${bold(name)} to .chunk/config.json`);
			} else if (resolved.saveAction === "prompt") {
				const shouldSave = await promptConfirm(`Save ${bold(name)} to .chunk/config.json?`);
				if (shouldSave) {
					saveCommand(projectDir, name, opts.cmd as string);
					console.log(`${green("✓")} Saved ${bold(name)} to .chunk/config.json`);
				}
			}
			renderExecution(name, resolved.result);
			return resolved.result.exitCode !== 0 ? 1 : 0;
		}
	}
}

export function registerValidateCommands(program: Command): void {
	const validate = program
		.command("validate")
		.description("Validate your project — run all commands or a single named command")
		.argument("[name]", "Command name to run (e.g. test, lint)")
		.option("--sandbox-id <id>", "Sandbox ID to run validation on (remote mode)")
		.option("--org-id <id>", "Organization ID (required with --sandbox-id)")
		.option("--dry-run", "Show commands that would run without executing them", false)
		.option("--list", "List all configured commands", false)
		.option("--cmd <command>", "Run an inline command instead of config")
		.option("--save", "Save --cmd to .chunk/config.json", false)
		.option("--force", "Ignore cache, always run", false)
		.option("--status", "Check cache only, don't execute", false)
		.option("--project <path>", "Override project directory")
		.action(async (name, opts) => {
			const projectDir = opts.project ?? process.cwd();

			// Guard: `chunk validate run` was a subcommand on older versions
			if (name === "run") {
				printError(
					'"chunk validate run" is no longer a subcommand',
					'Use bare "chunk validate" to run all commands',
				);
				process.exit(2);
			}

			// chunk validate --list
			if (opts.list) {
				renderList(projectDir);
				process.exit(0);
			}

			// chunk validate:<name> — run a single named command with caching
			if (name) {
				const exitCode = await handleSingleCommand(projectDir, name, {
					cmd: opts.cmd,
					save: opts.save,
					force: opts.force,
					status: opts.status,
				});
				process.exit(exitCode);
			}

			// chunk validate (bare) — run the full suite
			let mode: ValidateMode;

			if (opts.dryRun) {
				mode = { type: "dry-run" };
			} else if (opts.sandboxId) {
				if (!opts.orgId) {
					printError("Missing option", "--org-id is required with --sandbox-id");
					process.exit(2);
				}
				const token = requireToken();
				if (!token) process.exit(2);
				mode = { type: "remote", orgId: opts.orgId, sandboxId: opts.sandboxId, token };
			} else {
				mode = { type: "local" };
			}

			const result = await runValidate(
				projectDir,
				mode,
				(command) => process.stdout.write(`\n${bold("$")} ${command}\n`),
				(stdout, stderr) => {
					if (stdout) process.stdout.write(stdout);
					if (stderr) process.stderr.write(stderr);
				},
			);

			if (!result.ok) {
				process.exit(handleValidateError(result.error, result.hint).exitCode);
			}

			if ("dryRun" in result) {
				process.stdout.write(`\n${bold("Commands that would run")}\n\n`);
				for (const command of result.commands) {
					process.stdout.write(`  ${bold("$")} ${command}\n`);
				}
				process.stdout.write("\n");
				process.exit(0);
			}

			process.exit(renderValidateResult(result.results, result.skipped).exitCode);
		});

	validate
		.command("init")
		.description("Initialize hook config files and detect install/test commands for this repo")
		.option("--force", "Overwrite existing files and config", false)
		.option("--profile <name>", `Shell environment profile (${PROFILES.join(", ")})`, "enable")
		.option("--skip-env", "Skip shell environment update", false)
		.action(async (opts) =>
			process.exit(
				(
					await runValidateInit({
						force: opts.force,
						profile: opts.profile,
						skipEnv: opts.skipEnv,
					})
				).exitCode,
			),
		);
}
