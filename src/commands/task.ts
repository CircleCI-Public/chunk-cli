import type { Command } from "@commander-js/extra-typings";
import { runTaskConfigWizard } from "../core/task-config";
import { runTask } from "../core/task-run";

export { buildProjectSlug, mapVcsTypeToOrgType } from "../core/task-config";
export type { RunTaskOptions as RunCommandOptions } from "../core/task-run";

export function registerTaskCommands(program: Command): void {
	const task = program
		.command("task")
		.description("Trigger and manage CircleCI pipeline runs from the CLI");

	task
		.command("run")
		.description("Trigger a chunk run against a CircleCI pipeline definition")
		.addHelpText(
			"after",
			`
Triggers a CircleCI pipeline run using a definition configured in .chunk/run.json.
The --definition flag accepts either a configured name (e.g. "dev") or a raw
definition UUID. Even when a raw UUID is passed, .chunk/run.json must exist
because it supplies the org and project IDs needed by the CircleCI API.

Environment Variables:
  CIRCLE_TOKEN             Required. CircleCI personal API token.

Examples:
  # Run using a configured definition name
  chunk task run --definition dev --prompt "Fix the flaky test in auth.spec.ts"

  # Run on a specific branch with a new branch created for the changes
  chunk task run --definition prod --prompt "Refactor the payment module" --branch main --new-branch

  # Disable pipeline-as-tool mode
  chunk task run --definition dev --prompt "Add type annotations" --no-pipeline-as-tool

  # Run using a raw definition UUID (still requires .chunk/run.json)
  chunk task run --definition 550e8400-e29b-41d4-a716-446655440000 --prompt "Fix the flaky test"`,
		)
		.requiredOption(
			"--definition <name>",
			"definition name from .chunk/run.json, or a raw definition UUID",
		)
		.requiredOption("--prompt <text>", "prompt text to send to the agent")
		.option("--branch <branch>", "Git branch to check out (overrides definition default)")
		.option("--new-branch", "create a new branch for this run", false)
		.option("--pipeline-as-tool", "let the agent invoke the pipeline as a tool call", true)
		.action(async (options) => {
			process.exit((await runTask(options)).exitCode);
		});

	task
		.command("config")
		.description("Set up .chunk/run.json for this repository")
		.addHelpText(
			"after",
			`
Interactive wizard that creates .chunk/run.json in your repository root.
The file stores your CircleCI org, project, and pipeline definition IDs
so that "chunk task run" can trigger runs without extra flags.

Environment Variables:
  CIRCLE_TOKEN             Required. CircleCI personal API token.`,
		)
		.action(async () => process.exit((await runTaskConfigWizard()).exitCode));
}
