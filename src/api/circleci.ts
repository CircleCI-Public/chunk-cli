export interface CircleCIRunRequest {
	agent_type: "prompt";
	checkout_branch: string;
	definition_id: string;
	parameters: {
		"agent-type": "prompt";
		"run-pipeline-as-a-tool": boolean;
		"create-new-branch": boolean;
		"custom-prompt": string;
	};
	chunk_environment_id: string | null;
	trigger_source: string;
	stats: {
		prompt: string;
		checkout_branch: string;
	};
}

export interface CircleCIRunResponse {
	runId?: string;
	pipelineId?: string;
	messageId?: string;
	[key: string]: unknown;
}

export class CircleCIError extends Error {
	constructor(
		message: string,
		public statusCode?: number,
		public responseBody?: string,
	) {
		super(message);
		this.name = "CircleCIError";
	}
}

/**
 * Trigger a CircleCI chunk run via the API
 */
export async function triggerChunkRun(
	token: string,
	orgId: string,
	projectId: string,
	request: CircleCIRunRequest,
): Promise<CircleCIRunResponse> {
	const url = `https://circleci.com/api/v2/agents/org/${orgId}/project/${projectId}/runs`;

	let response: Response;
	try {
		response = await fetch(url, {
			method: "POST",
			headers: {
				Accept: "application/json",
				"Content-Type": "application/json",
				"Circle-Token": token,
			},
			body: JSON.stringify(request),
		});
	} catch (error) {
		throw new CircleCIError(
			`Failed to connect to CircleCI API: ${error instanceof Error ? error.message : String(error)}`,
		);
	}

	// Read response body
	const responseBody = await response.text();

	// Handle error responses
	if (!response.ok) {
		const errorMessages: Record<number, string> = {
			401: "Invalid CircleCI API token",
			403: "Access forbidden - check token permissions",
			404: "Resource not found - check org_id, project_id, or definition_id",
			429: "Rate limit exceeded - please try again later",
		};

		const message =
			errorMessages[response.status] ||
			(response.status >= 500
				? `CircleCI server error (${response.status}) - please try again later`
				: `CircleCI API request failed with status ${response.status}`);

		throw new CircleCIError(message, response.status, responseBody);
	}

	// Parse JSON response
	try {
		return JSON.parse(responseBody) as CircleCIRunResponse;
	} catch {
		throw new CircleCIError(
			"Invalid JSON response from CircleCI API",
			response.status,
			responseBody,
		);
	}
}
