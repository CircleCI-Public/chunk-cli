import * as fs from "node:fs";
import {
	CircleCIError,
	type CircleCIRunRequest,
	type CircleCIRunResponse,
	triggerChunkRun,
} from "../api/circleci";
import {
	getDefinitionByNameOrId,
	getRunConfigPath,
	loadRunConfig,
	type RunConfig,
	type RunDefinition,
	saveRunConfig,
	validateRunConfig,
} from "../storage/run-config";
import type { CommandResult } from "../types";
import { bold, cyan, dim, yellow } from "../ui/colors";
import { formatStep, label, printSuccess, printWarning } from "../ui/format";
import { promptConfirm, promptInput } from "../ui/prompt";
import { handleError, printError } from "../utils/errors";

export interface RunCommandOptions {
	definition: string;
	prompt: string;
	branch?: string;
	newBranch: boolean;
	pipelineAsTool: boolean;
}

export async function runTaskRun(options: RunCommandOptions): Promise<CommandResult> {
	const definition = options.definition;
	const token = process.env.CIRCLECI_TOKEN;
	if (!token) {
		printError(
			"CircleCI token not found",
			"CIRCLECI_TOKEN environment variable is not set.",
			"Set CIRCLECI_TOKEN to your CircleCI personal API token.",
		);
		return { exitCode: 2 };
	}

	let config: RunConfig;
	try {
		config = loadRunConfig();
	} catch (error) {
		const err = error instanceof Error ? error : new Error(String(error));
		printError(
			"Failed to load run configuration",
			err.message,
			"Ensure .chunk/run.json exists in your repository root.",
		);
		return { exitCode: 2 };
	}

	let resolved: ReturnType<typeof getDefinitionByNameOrId>;
	try {
		resolved = getDefinitionByNameOrId(config, definition);
	} catch (error) {
		const err = error instanceof Error ? error : new Error(String(error));
		const available = Object.keys(config.definitions).join(", ");
		printError(
			"Unknown definition",
			err.message,
			available ? `Available definitions: ${available}` : "No definitions found in .chunk/run.json",
		);
		return { exitCode: 2 };
	}

	const branch = options.branch ?? resolved.branch;
	const labelWidth = 18;

	console.log(`\n${bold("Triggering chunk run")}\n`);
	console.log(`${label("Definition:", labelWidth)} ${definition}`);
	console.log(`${label("Branch:", labelWidth)} ${branch}`);
	console.log(`${label("New branch:", labelWidth)} ${options.newBranch ? "yes" : "no"}`);
	console.log(`${label("Pipeline as tool:", labelWidth)} ${options.pipelineAsTool ? "yes" : "no"}`);
	console.log(`${label("Prompt:", labelWidth)} ${options.prompt}\n`);

	const request: CircleCIRunRequest = {
		agent_type: "prompt",
		checkout_branch: branch,
		definition_id: resolved.definitionId,
		parameters: {
			"agent-type": "prompt",
			"run-pipeline-as-a-tool": options.pipelineAsTool,
			"create-new-branch": options.newBranch,
			"custom-prompt": options.prompt,
		},
		chunk_environment_id: resolved.envId ?? null,
		trigger_source: "chunk-cli",
		stats: {
			prompt: options.prompt,
			checkout_branch: branch,
		},
	};

	let response: CircleCIRunResponse;
	try {
		response = await triggerChunkRun(token, config.org_id, config.project_id, request);
	} catch (error) {
		if (error instanceof CircleCIError) {
			printError(
				"CircleCI API error",
				error.message,
				"Check your CIRCLECI_TOKEN and .chunk/run.json configuration.",
			);
			return { exitCode: 2 };
		}
		handleError(error);
		return { exitCode: 2 };
	}

	printSuccess("Run triggered successfully!");
	if (response.runId) {
		console.log(`${dim("Run ID:")}      ${response.runId}`);
	}
	if (response.pipelineId) {
		console.log(`${dim("Pipeline ID:")} ${response.pipelineId}`);
	}
	console.log("");

	return { exitCode: 0 };
}

const UUID_REGEX = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function isValidUuid(value: string): boolean {
	return UUID_REGEX.test(value);
}

async function promptRequiredInput(message: string): Promise<string> {
	while (true) {
		const value = (await promptInput(message)).trim();
		if (value) return value;
		console.log(yellow("  This field is required."));
	}
}

async function promptUuid(message: string, required: boolean): Promise<string | null> {
	while (true) {
		const value = (await promptInput(message)).trim();
		if (!value && !required) return null;
		if (!value && required) {
			console.log(yellow("  This field is required."));
			continue;
		}
		if (!isValidUuid(value)) {
			console.log(yellow("  Must be a valid UUID (e.g. 550e8400-e29b-41d4-a716-446655440000)."));
			continue;
		}
		return value;
	}
}

async function collectDefinition(): Promise<{ name: string; definition: RunDefinition }> {
	const name = await promptRequiredInput("  Definition name (e.g. dev, prod): ");

	const definitionId = await promptUuid("  Definition ID (UUID from CircleCI): ", true);
	if (!definitionId) throw new Error("definition_id is required");

	const description = (await promptInput("  Description (optional): ")).trim() || undefined;
	const branchInput = (await promptInput("  Default branch [main]: ")).trim();
	const defaultBranch = branchInput || "main";
	const envId = await promptUuid("  Environment ID (optional UUID): ", false);

	const definition: RunDefinition = {
		definition_id: definitionId,
		...(description && { description }),
		default_branch: defaultBranch,
		...(envId !== null && { chunk_environment_id: envId }),
	};

	return { name, definition };
}
export async function runTaskConfig(): Promise<CommandResult> {
	console.log(`\n${bold("Chunk Run Setup")}\n`);
	console.log(`This wizard creates ${cyan(".chunk/run.json")} in your repository root.`);
	console.log(
		"You will need your CircleCI org ID, project ID, and at least one pipeline definition ID.\n",
	);

	// Check for git repo and existing config
	let configPath: string;
	try {
		configPath = getRunConfigPath();
	} catch (error) {
		const err = error instanceof Error ? error : new Error(String(error));
		printError(
			"Not in a git repository",
			err.message,
			"Run this command from within your project.",
		);
		return { exitCode: 2 };
	}

	if (fs.existsSync(configPath)) {
		printWarning(`${configPath} already exists.`);
		const overwrite = await promptConfirm("Overwrite the existing configuration?");
		if (!overwrite) {
			console.log("\nSetup cancelled.\n");
			return { exitCode: 0 };
		}
		console.log("");
	}

	// Collect org and project info
	console.log(`${formatStep(1, 3, "Organization & project")}\n`);
	console.log(dim("  Find these in CircleCI → Organization Settings → Overview\n"));

	const orgId = await promptRequiredInput("Organization ID: ");
	const projectId = await promptRequiredInput("Project ID: ");

	let orgType: "github" | "circleci" = "github";
	while (true) {
		const input = (await promptInput("Organization type (github/circleci) [github]: "))
			.trim()
			.toLowerCase();
		if (!input || input === "github") {
			orgType = "github";
			break;
		}
		if (input === "circleci") {
			orgType = "circleci";
			break;
		}
		console.log(yellow('  Must be "github" or "circleci".'));
	}

	// Collect definitions
	console.log(`\n${formatStep(2, 3, "Pipeline definitions")}\n`);
	console.log(
		dim(
			"  A definition maps a short name (e.g. dev, prod) to a CircleCI chunk pipeline definition.\n" +
				"  Find the definition UUID in CircleCI → your project → the chunk pipeline definition page.\n",
		),
	);

	const definitions: Record<string, RunDefinition> = {};

	do {
		const { name, definition } = await collectDefinition();
		if (definitions[name]) {
			printWarning(`Definition "${name}" already exists — overwriting.`);
		}
		definitions[name] = definition;
		console.log("");
	} while (await promptConfirm("Add another definition?"));

	// Validate and save
	console.log(`\n${formatStep(3, 3, "Saving configuration")}\n`);

	const rawConfig = { org_id: orgId, project_id: projectId, org_type: orgType, definitions };

	let config: RunConfig;
	try {
		config = validateRunConfig(rawConfig);
	} catch (error) {
		const err = error instanceof Error ? error : new Error(String(error));
		printError("Invalid configuration", err.message);
		return { exitCode: 2 };
	}

	try {
		saveRunConfig(config);
	} catch (error) {
		const err = error instanceof Error ? error : new Error(String(error));
		printError("Failed to save configuration", err.message);
		return { exitCode: 2 };
	}

	printSuccess("Configuration saved!");
	console.log(dim(`  ${configPath}\n`));

	const defNames = Object.keys(definitions).join(", ");
	console.log(`${bold("Next steps:")}`);
	console.log(`  Set ${cyan("CIRCLECI_TOKEN")} in your environment, then run:`);
	console.log(
		dim(
			`  chunk run --definition ${Object.keys(definitions)[0] ?? "<definition>"} --prompt "your task"\n`,
		),
	);
	if (Object.keys(definitions).length > 1) {
		console.log(dim(`  Available definitions: ${defNames}\n`));
	}

	return { exitCode: 0 };
}
