import * as fs from "node:fs";
import type { CircleCIRunRequest } from "../api/circleci";
import { triggerChunkRun } from "../api/circleci";
import type { RunConfig } from "../storage/run-config";
import {
	getDefinitionByNameOrId,
	getRunConfigPath,
	loadRunConfig,
	saveRunConfig,
	validateRunConfig,
} from "../storage/run-config";
import type { CommandResult, ParsedArgs } from "../types";
import { green, yellow } from "../ui/colors";
import { promptConfirm, promptInput } from "../ui/prompt";
import { printError } from "../utils/errors";

/**
 * Load config with standardized error handling
 */
function loadConfigOrFail(): RunConfig | null {
	try {
		return loadRunConfig();
	} catch (error) {
		console.log("");
		if (error instanceof Error && error.message.includes("not found")) {
			printError(
				"Run configuration not found",
				"No .chunk/run.json file found in the repository root.",
				"Run `chunk run init` to create the configuration file.",
			);
		} else {
			printError(
				"Failed to load run configuration",
				error instanceof Error ? error.message : String(error),
				"Check the .chunk/run.json file for errors.",
			);
		}
		return null;
	}
}

/**
 * Handle CircleCI API errors with specific messages
 */
function handleApiError(error: unknown): void {
	if (!(error instanceof Error)) {
		printError(
			"Failed to trigger chunk run",
			String(error),
			"Check the error details and try again.",
		);
		return;
	}

	const errorMap: Record<string, { brief: string; detail?: string; suggestion: string }> = {
		"Invalid CircleCI API token": {
			brief: "Invalid CircleCI API token",
			detail: "The provided API token is not valid.",
			suggestion: "Check your CIRCLECI_API_TOKEN environment variable.",
		},
		"Access forbidden": {
			brief: "Access forbidden",
			detail: "Your API token does not have permission to trigger runs.",
			suggestion: "Check your token permissions in CircleCI.",
		},
		"Resource not found": {
			brief: "Resource not found",
			detail: "The org_id, project_id, or definition_id could not be found.",
			suggestion: "Check your configuration in .chunk/run.json",
		},
		"Rate limit": {
			brief: "Rate limit exceeded",
			detail: "You have made too many requests to the CircleCI API.",
			suggestion: "Wait a moment and try again.",
		},
		"server error": {
			brief: "CircleCI server error",
			detail: "The CircleCI API is experiencing issues.",
			suggestion: "Try again in a few moments.",
		},
		"Failed to connect": {
			brief: "Network error",
			suggestion: "Check your internet connection and try again.",
		},
	};

	for (const [key, value] of Object.entries(errorMap)) {
		if (error.message.includes(key)) {
			printError(value.brief, value.detail || error.message, value.suggestion);
			return;
		}
	}

	printError(
		"Failed to trigger chunk run",
		error.message,
		"Check the error details and try again.",
	);
}

export function showRunHelp(): void {
	console.log(`
Usage: chunk run <command>

Trigger CircleCI chunk tasks

Commands:
  init                        Set up .chunk/run.json configuration
  list                        List available run definitions
  <name-or-uuid> --prompt     Trigger a chunk run

Options:
  --prompt <text>            Task prompt text (required for triggering)
  --environment <uuid>       Override chunk_environment_id
  --branch <name>            Override checkout branch (default: "main")
  --no-new-branch            Disable new branch creation (default: enabled)
  --no-pipeline-as-tool      Disable pipeline as tool (default: enabled)
  --trigger-source <source>  Override trigger source (default: "chunk-cli")
  -h, --help                 Show this help message

Examples:
  chunk run init                           Set up configuration
  chunk run list                           Show available definitions
  chunk run dev --prompt "Fix the bug"     Trigger run with "dev" definition
  chunk run <uuid> --prompt "Task"         Trigger run with UUID directly

Environment Variables:
  CIRCLECI_API_TOKEN    CircleCI API token (required for triggering runs)
`);
}

/**
 * Interactive setup for .chunk/run.json
 */
async function runRunInit(): Promise<CommandResult> {
	console.log("\nCircleCI Chunk Run Configuration Setup\n");

	// Check if config already exists
	let configPath: string;
	try {
		configPath = getRunConfigPath();
	} catch (_error) {
		printError(
			"Not in a git repository",
			"The run configuration must be created in a git repository root.",
			"Navigate to your git repository and try again.",
		);
		return { exitCode: 2 };
	}

	if (fs.existsSync(configPath)) {
		console.log(yellow(`Configuration file already exists at: ${configPath}\n`));
		if (!(await promptConfirm("Do you want to overwrite it?"))) {
			console.log("\nSetup cancelled.\n");
			return { exitCode: 0 };
		}
		console.log("");
	}

	// Prompt for required configuration values
	console.log("Enter your CircleCI organization and project details:\n");

	const orgType = (await promptInput("Organization type (github/circleci, default: github): ")).trim().toLowerCase() || "github";
	if (orgType !== "github" && orgType !== "circleci") {
		console.log("");
		printError(
			"Invalid organization type",
			"Organization type must be either 'github' or 'circleci'.",
			"Please try again with a valid organization type.",
		);
		return { exitCode: 2 };
	}

	const orgId = (await promptInput("Organization ID: ")).trim();
	if (!orgId) {
		console.log("");
		printError(
			"Organization ID cannot be empty",
			undefined,
			"Please provide a valid organization ID.",
		);
		return { exitCode: 2 };
	}

	const projectId = (await promptInput("Project ID: ")).trim();
	if (!projectId) {
		console.log("");
		printError("Project ID cannot be empty", undefined, "Please provide a valid project ID.");
		return { exitCode: 2 };
	}

	// First definition (required)
	console.log("\nAdd your first run definition:\n");
	const definitionName =
		(await promptInput("Definition name (default: 'default'): ")).trim() || "default";
	const definitionId = (await promptInput("Definition ID (UUID): ")).trim();
	if (!definitionId) {
		console.log("");
		printError("Definition ID cannot be empty", undefined, "Please provide a valid definition ID.");
		return { exitCode: 2 };
	}
	const environmentId = (await promptInput("Environment ID (UUID, optional): ")).trim();

	// Build and validate config
	const config: RunConfig = {
		org_id: orgId,
		project_id: projectId,
		org_type: orgType as "github" | "circleci",
		definitions: {
			[definitionName]: {
				definition_id: definitionId,
				chunk_environment_id: environmentId || null,
				default_branch: "main",
			},
		},
	};

	try {
		validateRunConfig(config);
		saveRunConfig(config);
	} catch (error) {
		console.log("");
		printError(
			error instanceof Error && error.message.includes("Invalid")
				? "Invalid configuration"
				: "Failed to save configuration",
			error instanceof Error ? error.message : String(error),
			"Check your input values and try again.",
		);
		return { exitCode: 2 };
	}

	console.log(green(`\n✓ Configuration saved to: ${configPath}\n`));
	console.log(`You can now trigger a run with:\n  chunk run ${definitionName} --prompt "your task"\n`);

	return { exitCode: 0 };
}

/**
 * List available run definitions
 */
async function runRunList(): Promise<CommandResult> {
	const config = loadConfigOrFail();
	if (!config) return { exitCode: 2 };

	const definitions = Object.entries(config.definitions);
	if (definitions.length === 0) {
		console.log(yellow("\nNo definitions found in .chunk/run.json\n"));
		console.log("Add definitions to the config file, then run this command again.\n");
		return { exitCode: 0 };
	}

	console.log("\nAvailable chunk run definitions (from .chunk/run.json):\n");

	for (const [name, def] of definitions) {
		console.log(`  ${green(name)}`);
		console.log(`    Definition ID: ${def.definition_id}`);
		console.log(`    Environment: ${def.chunk_environment_id || "(none)"}`);
		console.log(`    Default Branch: ${def.default_branch || "main"}`);
		if (def.description) {
			console.log(`    Description: ${def.description}`);
		}
		console.log("");
	}

	console.log("Run 'chunk run <name> --prompt \"your prompt\"' to trigger a run.\n");
	return { exitCode: 0 };
}

/**
 * Trigger a chunk run
 */
async function runRunTrigger(nameOrId: string, parsed: ParsedArgs): Promise<CommandResult> {
	// Validate required inputs
	const prompt = parsed.flags.prompt as string | undefined;
	if (!prompt?.trim()) {
		printError(
			"Missing required --prompt flag",
			"The --prompt flag is required to trigger a chunk run.",
			'Run `chunk run <name> --prompt "your task description"`',
		);
		return { exitCode: 2 };
	}

	const token = process.env.CIRCLECI_API_TOKEN;
	if (!token) {
		printError(
			"Missing CircleCI API token",
			"The CIRCLECI_API_TOKEN environment variable is not set.",
			"Set the token with: export CIRCLECI_API_TOKEN=your_token",
		);
		return { exitCode: 2 };
	}

	// Load configuration
	const config = loadConfigOrFail();
	if (!config) return { exitCode: 2 };

	// Resolve definition
	let definition: { definitionId: string; envId?: string | null; branch: string };
	try {
		definition = getDefinitionByNameOrId(config, nameOrId);
	} catch (error) {
		console.log("");
		printError(
			error instanceof Error ? error.message : String(error),
			"The definition name or UUID could not be found.",
			"Run `chunk run list` to see available definitions.",
		);
		return { exitCode: 2 };
	}

	// Build request
	const branch = (parsed.flags.branch as string | undefined) || definition.branch;
	const environment = (parsed.flags.environment as string | undefined) || definition.envId || null;

	const request: CircleCIRunRequest = {
		agent_type: "prompt",
		checkout_branch: branch,
		definition_id: definition.definitionId,
		parameters: {
			"agent-type": "prompt",
			"run-pipeline-as-a-tool": parsed.flags["no-pipeline-as-tool"] !== true,
			"create-new-branch": parsed.flags["no-new-branch"] !== true,
			"custom-prompt": prompt,
		},
		chunk_environment_id: environment,
		trigger_source: (parsed.flags["trigger-source"] as string | undefined) || "chunk-cli",
		stats: { prompt, checkout_branch: branch },
	};

	// Trigger the run
	try {
		const response = await triggerChunkRun(token, config.org_id, config.project_id, request);

		console.log(green("✓ Chunk run triggered successfully!\n"));

		if (response.runId) {
			const chatUrl = `https://app.circleci.com/agents/${config.org_type}/${config.org_id}/chat/${response.runId}`;
			console.log(`  ${chatUrl}\n`);
		}

		console.log("The agent will begin working on your prompt shortly.\n");

		return { exitCode: 0 };
	} catch (error) {
		console.log("");
		handleApiError(error);
		return { exitCode: 2 };
	}
}

/**
 * Main entry point for the run command
 */
export async function runRun(parsed: ParsedArgs): Promise<CommandResult> {
	if (parsed.flags.help) {
		showRunHelp();
		return { exitCode: 0 };
	}

	const subcommand = parsed.subcommand;

	if (!subcommand) {
		showRunHelp();
		return { exitCode: 2 };
	}

	switch (subcommand) {
		case "init":
			return runRunInit();
		case "list":
			return runRunList();
		default:
			// Treat as a name or UUID to trigger a run
			return runRunTrigger(subcommand, parsed);
	}
}
