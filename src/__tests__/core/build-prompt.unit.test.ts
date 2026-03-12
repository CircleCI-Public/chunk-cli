import { beforeEach, describe, expect, it, mock } from "bun:test";
import { deriveOutputPaths, resolveOrgAndRepos } from "../../core/build-prompt.steps";

// Mock the git-remote module. Bun scopes mock.module() to this file,
// so it does not affect other test files.
const mockDetectGitHubOrgAndRepo = mock(() =>
	Promise.resolve({ org: "detected-org", repo: "detected-repo" }),
);

mock.module("../../utils/git-remote", () => ({
	detectGitHubOrgAndRepo: mockDetectGitHubOrgAndRepo,
}));

describe("resolveOrgAndRepos", () => {
	beforeEach(() => {
		mockDetectGitHubOrgAndRepo.mockClear();
		mockDetectGitHubOrgAndRepo.mockResolvedValue({
			org: "detected-org",
			repo: "detected-repo",
		});
	});

	it("throws when org is provided without repos", async () => {
		const error = await resolveOrgAndRepos({ org: "my-org", repos: [] }).catch((e) => e);
		expect(error).toBeInstanceOf(Error);
		expect(error.message).toMatch("--repos is required when --org is provided");
	});

	it("returns org and repos as-is when both are provided", async () => {
		const result = await resolveOrgAndRepos({
			org: "my-org",
			repos: ["repo-a", "repo-b"],
		});

		expect(result).toEqual({ org: "my-org", repos: ["repo-a", "repo-b"] });
		expect(mockDetectGitHubOrgAndRepo).not.toHaveBeenCalled();
	});

	it("auto-detects org and repo when neither is provided", async () => {
		const result = await resolveOrgAndRepos({ repos: [] });

		expect(result).toEqual({
			org: "detected-org",
			repos: ["detected-repo"],
		});
		expect(mockDetectGitHubOrgAndRepo).toHaveBeenCalledTimes(1);
	});

	it("auto-detects org when only repos are provided", async () => {
		const result = await resolveOrgAndRepos({
			repos: ["custom-repo-1", "custom-repo-2"],
		});

		expect(result).toEqual({
			org: "detected-org",
			repos: ["custom-repo-1", "custom-repo-2"],
		});
		expect(mockDetectGitHubOrgAndRepo).toHaveBeenCalledTimes(1);
	});
});

describe("deriveOutputPaths", () => {
	it("strips .md extension and derives sibling paths", () => {
		const result = deriveOutputPaths("output/review-prompt.md");

		expect(result).toEqual({
			outputBase: "output/review-prompt",
			detailsPath: "output/review-prompt-details.json",
			analysisPath: "output/review-prompt-analysis.md",
		});
	});

	it("handles path without .md extension", () => {
		const result = deriveOutputPaths("output/review-prompt");

		expect(result).toEqual({
			outputBase: "output/review-prompt",
			detailsPath: "output/review-prompt-details.json",
			analysisPath: "output/review-prompt-analysis.md",
		});
	});

	it("handles simple filename", () => {
		const result = deriveOutputPaths("prompt.md");

		expect(result).toEqual({
			outputBase: "prompt",
			detailsPath: "prompt-details.json",
			analysisPath: "prompt-analysis.md",
		});
	});
});
