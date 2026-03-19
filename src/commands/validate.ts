import { PROFILES } from "@chunk/hook";
import type { Command } from "@commander-js/extra-typings";
import type { ValidateMode, ValidateStepResult } from "../core/validate";
import { runValidate } from "../core/validate";
import type { CommandResult } from "../types/index";
import { bold, dim, green, red, yellow } from "../ui/colors";
import { printError } from "../utils/errors";
import { requireToken } from "../utils/tokens";
import { runValidateInit } from "./validate/init";

const NO_COMMANDS_HINT = "Run `chunk validate init` to detect your install and test commands.";

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
	const validate = program.command("validate").description("Validate your project");

	validate
		.command("run")
		.description("Run configured validation commands locally or on a sandbox")
		.option("--sandbox-id <id>", "Sandbox ID to run validation on (remote mode)")
		.option("--org-id <id>", "Organization ID (required with --sandbox-id)")
		.option("--dry-run", "Show commands that would run without executing them", false)
		.action(async (options) => {
			let mode: ValidateMode;

			if (options.dryRun) {
				mode = { type: "dry-run" };
			} else if (options.sandboxId) {
				if (!options.orgId) {
					printError("Missing option", "--org-id is required with --sandbox-id");
					process.exit(2);
				}
				const token = requireToken();
				if (!token) process.exit(2);
				mode = { type: "remote", orgId: options.orgId, sandboxId: options.sandboxId, token };
			} else {
				mode = { type: "local" };
			}

			const result = await runValidate(
				process.cwd(),
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
