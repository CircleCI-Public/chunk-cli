/**
 * `chunk run` — standalone cached command execution.
 *
 * Runs named commands from `.chunk/commands.json`, caching results
 * keyed to git state. Supports inline commands with interactive save.
 *
 * Exit codes:
 *   0 — Pass (or cached)
 *   1 — Fail
 */

import type { Command } from "@commander-js/extra-typings";
import { runCommand, runList } from "../core/run";

export function registerRunCommand(program: Command): void {
	program
		.command("run")
		.description("Run a named command with cached results keyed to git state")
		.argument("[name]", "Command name (from .chunk/commands.json)")
		.option("--cmd <command>", "Run an inline command instead of config")
		.option("--save", "Save --cmd to .chunk/commands.json", false)
		.option("--force", "Ignore cache, always run", false)
		.option("--status", "Check cache only, don't execute", false)
		.option("--project <path>", "Override project directory")
		.action(async (name, opts) => {
			const projectDir = opts.project ?? process.cwd();

			if (!name) {
				await runList(projectDir);
				process.exit(0);
			}

			const exitCode = await runCommand(projectDir, name, {
				cmd: opts.cmd,
				save: opts.save,
				force: opts.force,
				status: opts.status,
			});

			process.exit(exitCode);
		});
}
