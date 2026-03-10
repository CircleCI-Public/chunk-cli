import {
	CircleCIError,
	type CircleCIRunRequest,
	type CircleCIRunResponse,
	triggerChunkRun,
} from "../api/circleci";
import { getCircleCIToken } from "../config";
import { getDefinitionByNameOrId, loadRunConfig, type RunConfig } from "../storage/run-config";
import type { CommandResult } from "../types";
import { bold, dim } from "../ui/colors";
import { label, printSuccess } from "../ui/format";
import { handleError, printError } from "../utils/errors";

export interface RunTaskOptions {
	definition: string;
	prompt: string;
	branch?: string;
	newBranch: boolean;
	pipelineAsTool: boolean;
}

export async function runTask(options: RunTaskOptions): Promise<CommandResult> {
	const definition = options.definition;
	const token = getCircleCIToken();
	if (!token) {
		printError(
			"CircleCI token not found",
			"CIRCLE_TOKEN environment variable is not set.",
			"Set CIRCLE_TOKEN to your CircleCI personal API token.",
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
				"Check your CIRCLE_TOKEN and .chunk/run.json configuration.",
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
