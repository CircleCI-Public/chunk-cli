import { afterEach, beforeEach, describe, expect, it, mock } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";
import { type RunTaskOptions, runTask } from "../../core/task-run";
import { saveRunConfig } from "../../storage/run-config";

const mockFetch = mock();
// @ts-expect-error - Mock doesn't fully implement fetch type
global.fetch = mockFetch;

const testDir = path.join(process.cwd(), ".test-core-task-run");
const originalCwd = process.cwd();
const originalToken = process.env.CIRCLECI_TOKEN;

const mockConfig = {
	org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
	project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
	org_type: "github" as const,
	definitions: {
		dev: {
			definition_id: "e2016e4e-0172-47b3-a4ea-a3ee1a592dba",
			chunk_environment_id: "b3c27e5f-1234-5678-9abc-def012345678",
			default_branch: "develop",
		},
		prod: {
			definition_id: "f3127f5f-0283-48c4-b5fb-b4ff2b693ccb",
			chunk_environment_id: null,
			default_branch: "main",
		},
	},
};

function mockSuccess(data = { runId: "run-123", pipelineId: "pipeline-456" }) {
	return { ok: true, status: 200, text: async () => JSON.stringify(data) } as Response;
}

function lastRequestBody() {
	// biome-ignore lint/style/noNonNullAssertion: test helper, always called after a successful mock invocation
	return JSON.parse(mockFetch.mock.calls[0]![1].body);
}

describe("runTask", () => {
	beforeEach(() => {
		fs.mkdirSync(path.join(testDir, ".git"), { recursive: true });
		process.chdir(testDir);
		process.env.CIRCLECI_TOKEN = "test-token";
		saveRunConfig(mockConfig);
		mockFetch.mockReset();
	});

	afterEach(() => {
		process.chdir(originalCwd);
		fs.rmSync(testDir, { recursive: true, force: true });
		if (originalToken !== undefined) {
			process.env.CIRCLECI_TOKEN = originalToken;
		} else {
			delete process.env.CIRCLECI_TOKEN;
		}
	});

	const baseOptions: RunTaskOptions = {
		definition: "dev",
		prompt: "fix the tests",
		newBranch: false,
		pipelineAsTool: true,
	};

	it("returns exitCode 2 when CIRCLECI_TOKEN is not set", async () => {
		delete process.env.CIRCLECI_TOKEN;

		const result = await runTask(baseOptions);

		expect(result.exitCode).toBe(2);
		expect(mockFetch).not.toHaveBeenCalled();
	});

	it("returns exitCode 2 when run.json does not exist", async () => {
		fs.rmSync(path.join(testDir, ".chunk", "run.json"));

		const result = await runTask(baseOptions);

		expect(result.exitCode).toBe(2);
		expect(mockFetch).not.toHaveBeenCalled();
	});

	it("returns exitCode 2 for unknown definition name", async () => {
		const result = await runTask({ ...baseOptions, definition: "staging" });

		expect(result.exitCode).toBe(2);
		expect(mockFetch).not.toHaveBeenCalled();
	});

	it("resolves a named definition to its definition_id", async () => {
		mockFetch.mockImplementation(async () => mockSuccess());

		await runTask({ ...baseOptions, definition: "dev" });

		expect(lastRequestBody().definition_id).toBe("e2016e4e-0172-47b3-a4ea-a3ee1a592dba");
	});

	it("uses the definition's default branch when --branch is not specified", async () => {
		mockFetch.mockImplementation(async () => mockSuccess());

		await runTask({ ...baseOptions, definition: "dev" });

		expect(lastRequestBody().checkout_branch).toBe("develop");
	});

	it("overrides branch with --branch flag", async () => {
		mockFetch.mockImplementation(async () => mockSuccess());

		await runTask({ ...baseOptions, branch: "feature/my-branch" });

		expect(lastRequestBody().checkout_branch).toBe("feature/my-branch");
	});

	it("accepts a raw UUID as --definition and passes it directly", async () => {
		mockFetch.mockImplementation(async () => mockSuccess());
		const uuid = "a1b2c3d4-5678-90ab-cdef-1234567890ab";

		await runTask({ ...baseOptions, definition: uuid });

		expect(lastRequestBody().definition_id).toBe(uuid);
	});

	it("defaults to 'main' branch when using a raw UUID definition", async () => {
		mockFetch.mockImplementation(async () => mockSuccess());
		const uuid = "a1b2c3d4-5678-90ab-cdef-1234567890ab";

		await runTask({ ...baseOptions, definition: uuid });

		expect(lastRequestBody().checkout_branch).toBe("main");
	});

	it("passes prompt, newBranch, and pipelineAsTool through to the API", async () => {
		mockFetch.mockImplementation(async () => mockSuccess());

		await runTask({
			definition: "dev",
			prompt: "refactor the auth module",
			newBranch: true,
			pipelineAsTool: false,
		});

		const body = lastRequestBody();
		expect(body.parameters["custom-prompt"]).toBe("refactor the auth module");
		expect(body.parameters["create-new-branch"]).toBe(true);
		expect(body.parameters["run-pipeline-as-a-tool"]).toBe(false);
	});

	it("includes environment ID from named definition", async () => {
		mockFetch.mockImplementation(async () => mockSuccess());

		await runTask({ ...baseOptions, definition: "dev" });

		expect(lastRequestBody().chunk_environment_id).toBe("b3c27e5f-1234-5678-9abc-def012345678");
	});

	it("sends null environment ID when definition has no environment", async () => {
		mockFetch.mockImplementation(async () => mockSuccess());

		await runTask({ ...baseOptions, definition: "prod" });

		expect(lastRequestBody().chunk_environment_id).toBeNull();
	});

	it("returns exitCode 0 on success", async () => {
		mockFetch.mockImplementation(async () => mockSuccess());

		const result = await runTask(baseOptions);

		expect(result.exitCode).toBe(0);
	});

	it("returns exitCode 2 on CircleCI API error", async () => {
		mockFetch.mockImplementation(async () => ({
			ok: false,
			status: 401,
			text: async () => "Unauthorized",
		}));

		const result = await runTask(baseOptions);

		expect(result.exitCode).toBe(2);
	});
});
