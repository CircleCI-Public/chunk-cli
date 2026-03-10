import { beforeEach, describe, expect, it, mock } from "bun:test";
import { resolveOrgAndRepos } from "../../core/build-prompt";

// Mock the git-remote module
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
		await expect(resolveOrgAndRepos({ org: "my-org", repos: [] })).rejects.toThrow(
			"--repos is required when --org is provided",
		);
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
