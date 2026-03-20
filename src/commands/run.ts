/**
 * `chunk run <name>` — general-purpose cached command execution.
 *
 * Runs a named command from `.chunk/config.yml`, caching the result
 * keyed to git state. Works from terminal, git hooks, or CI.
 *
 * Exit codes:
 *   0 — Pass (or skip/fresh)
 *   1 — Fail
 */

import type { Command } from "@commander-js/extra-typings";
import { runNamedCommand } from "@chunk/hook";

export function registerRunCommand(program: Command): void {
	program
		.command("run")
		.description("Run a named command with cached results keyed to git state")
		.argument("<name>", "Command name (matches config key in .chunk/config.yml)")
		.option("--force", "Ignore cache, always run", false)
		.option("--status", "Check cache only, don't execute", false)
		.option("--staged", "Only consider staged files for change detection", false)
		.option("--project <path>", "Override project directory")
		.action(async (name, opts) => {
			const projectDir = opts.project ?? process.cwd();

			const result = await runNamedCommand(projectDir, name, {
				force: opts.force,
				status: opts.status,
				staged: opts.staged,
			});

			switch (result.status) {
				case "fresh":
					console.log(`✓ ${name}: cached result is fresh (pass)`);
					process.exit(0);
					break;
				case "skip-no-changes":
					console.log(`✓ ${name}: no relevant files changed, skipped`);
					process.exit(0);
					break;
				case "pass":
					console.log(`✓ ${name}: pass`);
					if (result.output) {
						process.stdout.write(result.output);
						if (!result.output.endsWith("\n")) process.stdout.write("\n");
					}
					process.exit(0);
					break;
				case "fail":
					console.error(`✗ ${name}: fail (exit ${result.exitCode})`);
					if (result.output) {
						process.stderr.write(result.output);
						if (!result.output.endsWith("\n")) process.stderr.write("\n");
					}
					process.exit(1);
					break;
			}
		});
}
