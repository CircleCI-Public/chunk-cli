import { describe, expect, it } from "bun:test";
import { buildProjectSlug, mapVcsTypeToOrgType } from "../commands/task";

describe("mapVcsTypeToOrgType", () => {
	it("should map 'github' to 'github'", () => {
		expect(mapVcsTypeToOrgType("github")).toBe("github");
	});

	it("should map 'gh' to 'github'", () => {
		expect(mapVcsTypeToOrgType("gh")).toBe("github");
	});

	it("should map 'GitHub' to 'github' (case insensitive)", () => {
		expect(mapVcsTypeToOrgType("GitHub")).toBe("github");
	});

	it("should map 'bitbucket' to 'circleci'", () => {
		expect(mapVcsTypeToOrgType("bitbucket")).toBe("circleci");
	});

	it("should map unknown types to 'circleci'", () => {
		expect(mapVcsTypeToOrgType("gitlab")).toBe("circleci");
	});
});

describe("buildProjectSlug", () => {
	it("should build slug for github projects", () => {
		expect(buildProjectSlug("github", "my-org", "my-repo")).toBe("gh/my-org/my-repo");
	});

	it("should build slug for bitbucket projects", () => {
		expect(buildProjectSlug("bitbucket", "my-org", "my-repo")).toBe("bb/my-org/my-repo");
	});

	it("should default to 'gh' prefix for unknown vcs types", () => {
		expect(buildProjectSlug("gitlab", "my-org", "my-repo")).toBe("gh/my-org/my-repo");
	});
});
