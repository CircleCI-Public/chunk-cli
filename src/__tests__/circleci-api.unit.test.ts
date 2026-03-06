import { afterEach, describe, expect, it, mock } from "bun:test";
import {
	CircleCIError,
	type CircleCIRunRequest,
	fetchCollaborations,
	fetchFollowedProjects,
	fetchProjectBySlug,
	triggerChunkRun,
} from "../api/circleci";

// Mock fetch globally
const mockFetch = mock();
// @ts-expect-error - Mock doesn't fully implement fetch type
global.fetch = mockFetch;

describe("CircleCI API Client", () => {
	const mockToken = "test-token";
	const mockOrgId = "a37b44de-e4f8-4d09-956a-9c1148f3adf5";
	const mockProjectId = "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb";

	const mockRequest: CircleCIRunRequest = {
		agent_type: "prompt",
		checkout_branch: "main",
		definition_id: "e2016e4e-0172-47b3-a4ea-a3ee1a592dba",
		parameters: {
			"agent-type": "prompt",
			"run-pipeline-as-a-tool": true,
			"create-new-branch": true,
			"custom-prompt": "test prompt",
		},
		chunk_environment_id: null,
		trigger_source: "chunk-cli",
		stats: {
			prompt: "test prompt",
			checkout_branch: "main",
		},
	};

	afterEach(() => {
		mockFetch.mockReset();
	});

	describe("triggerChunkRun", () => {
		it("should make a POST request with correct headers and body", async () => {
			const mockResponse = {
				ok: true,
				status: 200,
				text: async () => JSON.stringify({ runId: "run-123", pipelineId: "pipeline-456" }),
			} as Response;

			mockFetch.mockImplementation(async () => mockResponse);

			await triggerChunkRun(mockToken, mockOrgId, mockProjectId, mockRequest);

			expect(mockFetch).toHaveBeenCalledWith(
				`https://circleci.com/api/v2/agents/org/${mockOrgId}/project/${mockProjectId}/runs`,
				{
					method: "POST",
					headers: {
						Accept: "application/json",
						"Content-Type": "application/json",
						"Circle-Token": mockToken,
					},
					body: JSON.stringify(mockRequest),
				},
			);
		});

		it("should return parsed response on success", async () => {
			const mockResponseData = {
				runId: "run-123",
				pipelineId: "pipeline-456",
				messageId: "msg-789",
			};
			const mockResponse = {
				ok: true,
				status: 200,
				text: async () => JSON.stringify(mockResponseData),
			} as Response;

			mockFetch.mockImplementation(async () => mockResponse);

			const result = await triggerChunkRun(mockToken, mockOrgId, mockProjectId, mockRequest);

			expect(result).toEqual(mockResponseData);
		});

		it("should handle network errors", async () => {
			mockFetch.mockImplementation(async () => {
				throw new Error("Network error");
			});

			try {
				await triggerChunkRun(mockToken, mockOrgId, mockProjectId, mockRequest);
				expect.unreachable("Should have thrown an error");
			} catch (error) {
				expect(error).toBeInstanceOf(CircleCIError);
				expect((error as CircleCIError).message).toMatch(/Failed to connect to CircleCI API/);
			}
		});

		it("should throw CircleCIError for 401 Unauthorized", async () => {
			const mockResponse = {
				ok: false,
				status: 401,
				text: async () => "Unauthorized",
			} as Response;

			mockFetch.mockImplementation(async () => mockResponse);

			await expect(
				triggerChunkRun(mockToken, mockOrgId, mockProjectId, mockRequest),
			).rejects.toThrow(/Invalid CircleCI API token/);
		});

		it("should throw CircleCIError for 403 Forbidden", async () => {
			const mockResponse = {
				ok: false,
				status: 403,
				text: async () => "Forbidden",
			} as Response;

			mockFetch.mockImplementation(async () => mockResponse);

			await expect(
				triggerChunkRun(mockToken, mockOrgId, mockProjectId, mockRequest),
			).rejects.toThrow(/Access forbidden/);
		});

		it("should throw CircleCIError for 404 Not Found", async () => {
			const mockResponse = {
				ok: false,
				status: 404,
				text: async () => "Not Found",
			} as Response;

			mockFetch.mockImplementation(async () => mockResponse);

			await expect(
				triggerChunkRun(mockToken, mockOrgId, mockProjectId, mockRequest),
			).rejects.toThrow(/Resource not found/);
		});

		it("should throw CircleCIError for 429 Rate Limit", async () => {
			const mockResponse = {
				ok: false,
				status: 429,
				text: async () => "Too Many Requests",
			} as Response;

			mockFetch.mockImplementation(async () => mockResponse);

			await expect(
				triggerChunkRun(mockToken, mockOrgId, mockProjectId, mockRequest),
			).rejects.toThrow(/Rate limit exceeded/);
		});

		it("should throw CircleCIError for 500 Server Error", async () => {
			const mockResponse = {
				ok: false,
				status: 500,
				text: async () => "Internal Server Error",
			} as Response;

			mockFetch.mockImplementation(async () => mockResponse);

			await expect(
				triggerChunkRun(mockToken, mockOrgId, mockProjectId, mockRequest),
			).rejects.toThrow(/CircleCI server error/);
		});

		it("should throw CircleCIError for 502 Bad Gateway", async () => {
			const mockResponse = {
				ok: false,
				status: 502,
				text: async () => "Bad Gateway",
			} as Response;

			mockFetch.mockImplementation(async () => mockResponse);

			await expect(
				triggerChunkRun(mockToken, mockOrgId, mockProjectId, mockRequest),
			).rejects.toThrow(/CircleCI server error \(502\)/);
		});

		it("should throw CircleCIError for unmapped non-5xx status (418)", async () => {
			const mockResponse = {
				ok: false,
				status: 418,
				text: async () => "I'm a teapot",
			} as Response;

			mockFetch.mockImplementation(async () => mockResponse);

			try {
				await triggerChunkRun(mockToken, mockOrgId, mockProjectId, mockRequest);
				expect.unreachable("Should have thrown an error");
			} catch (error) {
				expect(error).toBeInstanceOf(CircleCIError);
				const circleCIError = error as CircleCIError;
				expect(circleCIError.message).toMatch(/CircleCI API request failed with status 418/);
				expect(circleCIError.statusCode).toBe(418);
			}
		});

		it("should throw CircleCIError for invalid JSON response", async () => {
			const mockResponse = {
				ok: true,
				status: 200,
				text: async () => "invalid json",
			} as Response;

			mockFetch.mockImplementation(async () => mockResponse);

			try {
				await triggerChunkRun(mockToken, mockOrgId, mockProjectId, mockRequest);
				expect.unreachable("Should have thrown an error");
			} catch (error) {
				expect(error).toBeInstanceOf(CircleCIError);
				const circleCIError = error as CircleCIError;
				expect(circleCIError.message).toMatch(/Invalid JSON response/);
				expect(circleCIError.statusCode).toBe(200);
				expect(circleCIError.responseBody).toBe("invalid json");
			}
		});

		it("should include status code and response body in CircleCIError", async () => {
			const mockResponse = {
				ok: false,
				status: 400,
				text: async () => '{"error": "Bad Request"}',
			} as Response;

			mockFetch.mockImplementation(async () => mockResponse);

			try {
				await triggerChunkRun(mockToken, mockOrgId, mockProjectId, mockRequest);
				expect.unreachable("Should have thrown an error");
			} catch (error) {
				expect(error).toBeInstanceOf(CircleCIError);
				const circleCIError = error as CircleCIError;
				expect(circleCIError.statusCode).toBe(400);
				expect(circleCIError.responseBody).toBe('{"error": "Bad Request"}');
			}
		});
	});

	describe("fetchFollowedProjects", () => {
		it("should fetch and return followed projects", async () => {
			const mockProjects = [
				{ vcs_type: "github", username: "my-org", reponame: "repo-a" },
				{ vcs_type: "github", username: "my-org", reponame: "repo-b" },
			];
			mockFetch.mockImplementation(async () => ({
				ok: true,
				status: 200,
				text: async () => JSON.stringify(mockProjects),
			}));

			const result = await fetchFollowedProjects(mockToken);

			expect(result).toEqual(mockProjects);
			expect(mockFetch).toHaveBeenCalledWith("https://circleci.com/api/v1.1/projects", {
				headers: {
					Accept: "application/json",
					"Circle-Token": mockToken,
				},
			});
		});

		it("should throw CircleCIError for 401", async () => {
			mockFetch.mockImplementation(async () => ({
				ok: false,
				status: 401,
				text: async () => "Unauthorized",
			}));

			await expect(fetchFollowedProjects(mockToken)).rejects.toThrow(/Invalid CircleCI API token/);
		});

		it("should throw CircleCIError for 403", async () => {
			mockFetch.mockImplementation(async () => ({
				ok: false,
				status: 403,
				text: async () => "Forbidden",
			}));

			await expect(fetchFollowedProjects(mockToken)).rejects.toThrow(/Access forbidden/);
		});

		it("should handle network errors", async () => {
			mockFetch.mockImplementation(async () => {
				throw new Error("Connection refused");
			});

			try {
				await fetchFollowedProjects(mockToken);
				expect.unreachable("Should have thrown");
			} catch (error) {
				expect(error).toBeInstanceOf(CircleCIError);
				expect((error as CircleCIError).message).toMatch(/Failed to connect/);
			}
		});

		it("should throw CircleCIError for invalid JSON", async () => {
			mockFetch.mockImplementation(async () => ({
				ok: true,
				status: 200,
				text: async () => "not json",
			}));

			try {
				await fetchFollowedProjects(mockToken);
				expect.unreachable("Should have thrown");
			} catch (error) {
				expect(error).toBeInstanceOf(CircleCIError);
				expect((error as CircleCIError).message).toMatch(/Invalid JSON response/);
			}
		});
	});

	describe("fetchCollaborations", () => {
		it("should fetch and return collaborations", async () => {
			const mockCollabs = [
				{ id: "org-uuid-1", name: "my-org", "vcs-type": "github", slug: "gh/my-org" },
				{
					id: "org-uuid-2",
					name: "another-org",
					"vcs-type": "bitbucket",
					slug: "bb/another-org",
				},
			];
			mockFetch.mockImplementation(async () => ({
				ok: true,
				status: 200,
				text: async () => JSON.stringify(mockCollabs),
			}));

			const result = await fetchCollaborations(mockToken);

			expect(result).toEqual(mockCollabs);
			expect(mockFetch).toHaveBeenCalledWith("https://circleci.com/api/v2/me/collaborations", {
				headers: {
					Accept: "application/json",
					"Circle-Token": mockToken,
				},
			});
		});

		it("should throw CircleCIError for 401", async () => {
			mockFetch.mockImplementation(async () => ({
				ok: false,
				status: 401,
				text: async () => "Unauthorized",
			}));

			await expect(fetchCollaborations(mockToken)).rejects.toThrow(/Invalid CircleCI API token/);
		});

		it("should handle network errors", async () => {
			mockFetch.mockImplementation(async () => {
				throw new Error("DNS resolution failed");
			});

			try {
				await fetchCollaborations(mockToken);
				expect.unreachable("Should have thrown");
			} catch (error) {
				expect(error).toBeInstanceOf(CircleCIError);
				expect((error as CircleCIError).message).toMatch(/Failed to connect/);
			}
		});

		it("should throw CircleCIError for invalid JSON", async () => {
			mockFetch.mockImplementation(async () => ({
				ok: true,
				status: 200,
				text: async () => "<html>error</html>",
			}));

			try {
				await fetchCollaborations(mockToken);
				expect.unreachable("Should have thrown");
			} catch (error) {
				expect(error).toBeInstanceOf(CircleCIError);
				expect((error as CircleCIError).message).toMatch(/Invalid JSON response/);
			}
		});
	});

	describe("fetchProjectBySlug", () => {
		it("should fetch project details by slug", async () => {
			const mockDetails = {
				id: "project-uuid",
				organization_id: "org-uuid",
				name: "my-repo",
				slug: "gh/my-org/my-repo",
			};
			mockFetch.mockImplementation(async () => ({
				ok: true,
				status: 200,
				text: async () => JSON.stringify(mockDetails),
			}));

			const result = await fetchProjectBySlug(mockToken, "gh/my-org/my-repo");

			expect(result).toEqual(mockDetails);
			expect(mockFetch).toHaveBeenCalledWith(
				"https://circleci.com/api/v2/project/gh/my-org/my-repo",
				{
					headers: {
						Accept: "application/json",
						"Circle-Token": mockToken,
					},
				},
			);
		});

		it("should throw CircleCIError for 404", async () => {
			mockFetch.mockImplementation(async () => ({
				ok: false,
				status: 404,
				text: async () => "Not Found",
			}));

			await expect(fetchProjectBySlug(mockToken, "gh/no-org/no-repo")).rejects.toThrow(
				/Resource not found/,
			);
		});

		it("should throw CircleCIError for 401", async () => {
			mockFetch.mockImplementation(async () => ({
				ok: false,
				status: 401,
				text: async () => "Unauthorized",
			}));

			await expect(fetchProjectBySlug(mockToken, "gh/org/repo")).rejects.toThrow(
				/Invalid CircleCI API token/,
			);
		});

		it("should handle network errors", async () => {
			mockFetch.mockImplementation(async () => {
				throw new Error("Timeout");
			});

			try {
				await fetchProjectBySlug(mockToken, "gh/org/repo");
				expect.unreachable("Should have thrown");
			} catch (error) {
				expect(error).toBeInstanceOf(CircleCIError);
				expect((error as CircleCIError).message).toMatch(/Failed to connect/);
			}
		});

		it("should throw CircleCIError for invalid JSON", async () => {
			mockFetch.mockImplementation(async () => ({
				ok: true,
				status: 200,
				text: async () => "}{invalid",
			}));

			try {
				await fetchProjectBySlug(mockToken, "gh/org/repo");
				expect.unreachable("Should have thrown");
			} catch (error) {
				expect(error).toBeInstanceOf(CircleCIError);
				expect((error as CircleCIError).message).toMatch(/Invalid JSON response/);
			}
		});

		it("should throw CircleCIError for 500 server error", async () => {
			mockFetch.mockImplementation(async () => ({
				ok: false,
				status: 500,
				text: async () => "Internal Server Error",
			}));

			await expect(fetchProjectBySlug(mockToken, "gh/org/repo")).rejects.toThrow(
				/CircleCI server error/,
			);
		});
	});

	describe("CircleCIError", () => {
		it("should create error with message only", () => {
			const error = new CircleCIError("Test error");

			expect(error.message).toBe("Test error");
			expect(error.name).toBe("CircleCIError");
			expect(error.statusCode).toBeUndefined();
			expect(error.responseBody).toBeUndefined();
		});

		it("should create error with status code", () => {
			const error = new CircleCIError("Test error", 404);

			expect(error.message).toBe("Test error");
			expect(error.statusCode).toBe(404);
			expect(error.responseBody).toBeUndefined();
		});

		it("should create error with status code and response body", () => {
			const error = new CircleCIError("Test error", 500, "Server error details");

			expect(error.message).toBe("Test error");
			expect(error.statusCode).toBe(500);
			expect(error.responseBody).toBe("Server error details");
		});

		it("should be an instance of Error", () => {
			const error = new CircleCIError("Test error");

			expect(error).toBeInstanceOf(Error);
		});
	});
});
