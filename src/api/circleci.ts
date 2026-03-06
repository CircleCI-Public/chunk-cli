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

export interface Sandbox {
	id: string;
	name: string;
	organization_id: string;
	image?: string;
	[key: string]: unknown;
}

export interface SandboxListResponse {
	items: Sandbox[];
	[key: string]: unknown;
}

export interface SandboxAccessTokenResponse {
	access_token: string;
	[key: string]: unknown;
}

export interface ExecCommandResponse {
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

async function circleciRequest<T>(
	url: string,
	options: RequestInit,
): Promise<T> {
	let response: Response;
	try {
		response = await fetch(url, options);
	} catch (error) {
		throw new CircleCIError(
			`Failed to connect to CircleCI API: ${error instanceof Error ? error.message : String(error)}`,
		);
	}

	const responseBody = await response.text();

	if (!response.ok) {
		throw new CircleCIError(
			`CircleCI API request failed with status ${response.status}`,
			response.status,
			responseBody,
		);
	}

	try {
		return JSON.parse(responseBody) as T;
	} catch {
		throw new CircleCIError(
			"Invalid JSON response from CircleCI API",
			response.status,
			responseBody,
		);
	}
}

/**
 * List sandboxes for an organization
 */
export async function listSandboxesForOrg(
	orgId: string,
	token: string,
): Promise<SandboxListResponse> {
	return circleciRequest<SandboxListResponse>(
		`https://circleci.com/api/v2/sandboxes?org_id=${orgId}`,
		{
			headers: {
				Accept: "application/json",
				"Circle-Token": token,
			},
		},
	);
}

/**
 * Create a new sandbox
 */
export async function createSandbox(
	organizationId: string,
	name: string,
	token: string,
	image?: string,
): Promise<Sandbox> {
	return circleciRequest<Sandbox>("https://circleci.com/api/v2/sandboxes", {
		method: "POST",
		headers: {
			Accept: "application/json",
			"Content-Type": "application/json",
			"Circle-Token": token,
		},
		body: JSON.stringify({ organization_id: organizationId, name, ...(image && { image }) }),
	});
}

/**
 * Create an access token for a sandbox
 */
export async function createSandboxAccessToken(
	sandboxId: string,
	organizationId: string,
	token: string,
): Promise<SandboxAccessTokenResponse> {
	return circleciRequest<SandboxAccessTokenResponse>(
		`https://circleci.com/api/v2/sandboxes/${sandboxId}/access_token`,
		{
			method: "POST",
			headers: {
				Accept: "application/json",
				"Content-Type": "application/json",
				"Circle-Token": token,
			},
			body: JSON.stringify({ organization_id: organizationId }),
		},
	);
}

/**
 * Execute a command in a sandbox
 */
export async function execCommand(
	command: string,
	args: string[],
	accessToken: string,
): Promise<ExecCommandResponse> {
	return circleciRequest<ExecCommandResponse>(
		"https://circleci.com/api/v2/sandboxes/exec",
		{
			method: "POST",
			headers: {
				Accept: "application/json",
				"Content-Type": "application/json",
				Authorization: `Bearer ${accessToken}`,
			},
			body: JSON.stringify({ command, args }),
		},
	);
}
