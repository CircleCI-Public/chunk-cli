import type { Command } from "@commander-js/extra-typings";
import { runTaskConfigWizard } from "../core/task-config";
import { runTask } from "../core/task-run";

export { buildProjectSlug, mapVcsTypeToOrgType } from "../core/task-config";
export type { RunTaskOptions as RunCommandOptions } from "../core/task-run";

export function registerTaskCommands(program: Command): void {
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
			process.exit((await runTask(options)).exitCode);
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
		.action(async () => process.exit((await runTaskConfigWizard()).exitCode));
}

/** @deprecated Use runTask from core/task-run.ts directly */
export const runTaskRun = runTask;

/** @deprecated Use runTaskConfigWizard from core/task-config.ts directly */
export const runTaskConfig = runTaskConfigWizard;
