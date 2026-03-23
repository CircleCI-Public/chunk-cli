/**
 * `chunk validate` — run the full validation sequence.
 * `chunk validate:<name>` — run a single named command with caching.
 * `chunk validate:init` — auto-detect and set up commands.
 *
 * Colon syntax is rewritten to positional args in `src/index.ts`
 * before Commander parses, so this registers a single `validate`
 * command with an optional `[name]` argument.
 */

import { PROFILES } from "@chunk/hook";
import type { Command } from "@commander-js/extra-typings";
import { runCommand, runList } from "../core/run";
import type { ValidateMode, ValidateStepResult } from "../core/validate";
import { runValidate } from "../core/validate";
import type { CommandResult } from "../types/index";
import { bold, dim, green, red, yellow } from "../ui/colors";
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

function handleValidateError(error: string, hint?: string): CommandResult {
	const resolvedHint =
		hint ?? (error === "No validate commands configured" ? NO_COMMANDS_HINT : undefined);
	printError(error, resolvedHint);
	return { exitCode: 1 };
}

export function registerValidateCommands(program: Command): void {
	program
		.command("validate")
		.description("Validate your project — run sequence or a single command")
		.argument("[name]", "Command name (e.g. test, lint) or 'init'")
		// Sequence flags (bare `chunk validate`)
		.option("--sandbox-id <id>", "Sandbox ID to run validation on (remote mode)")
		.option("--org-id <id>", "Organization ID (required with --sandbox-id)")
		.option("--dry-run", "Show commands that would run without executing them", false)
		.option("--list", "List all configured commands", false)
		// Single-command flags (`chunk validate:<name>`)
		.option("--cmd <command>", "Run an inline command instead of config")
		.option("--save", "Save --cmd to .chunk/commands.json", false)
		.option("--force", "Ignore cache, always run", false)
		.option("--status", "Check cache only, don't execute", false)
		.option("--project <path>", "Override project directory")
		// Init flags (`chunk validate:init`)
		.option("--profile <name>", `Shell environment profile (${PROFILES.join(", ")})`, "enable")
		.option("--skip-env", "Skip shell environment update", false)
		.action(async (name, opts) => {
			const projectDir = opts.project ?? process.cwd();

			// chunk validate --list
			if (opts.list) {
				await runList(projectDir);
				process.exit(0);
			}

			// chunk validate:init
			if (name === "init") {
				const result = await runValidateInit({
					force: opts.force,
					profile: opts.profile,
					skipEnv: opts.skipEnv,
				});
				process.exit(result.exitCode);
			}

			// chunk validate:<name> — run a single command with caching
			if (name) {
				const exitCode = await runCommand(projectDir, name, {
					cmd: opts.cmd,
					save: opts.save,
					force: opts.force,
					status: opts.status,
				});
				process.exit(exitCode);
			}

			// chunk validate (bare) — run the full sequence
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
}
