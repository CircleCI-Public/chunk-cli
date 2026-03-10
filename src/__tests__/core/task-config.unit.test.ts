import { describe, expect, it } from "bun:test";
import { buildProjectSlug, mapVcsTypeToOrgType } from "../../core/task-config";

describe("mapVcsTypeToOrgType", () => {
	it("maps 'github' to 'github'", () => {
		expect(mapVcsTypeToOrgType("github")).toBe("github");
	});

	it("maps 'gh' to 'github'", () => {
		expect(mapVcsTypeToOrgType("gh")).toBe("github");
	});

	it("maps 'GitHub' to 'github' (case insensitive)", () => {
		expect(mapVcsTypeToOrgType("GitHub")).toBe("github");
	});

	it("maps 'bitbucket' to 'circleci'", () => {
		expect(mapVcsTypeToOrgType("bitbucket")).toBe("circleci");
	});

	it("maps unknown types to 'circleci'", () => {
		expect(mapVcsTypeToOrgType("gitlab")).toBe("circleci");
	});

	it("maps undefined to 'circleci'", () => {
		expect(mapVcsTypeToOrgType(undefined)).toBe("circleci");
	});
});

describe("buildProjectSlug", () => {
	it("builds slug for github projects", () => {
		expect(buildProjectSlug("github", "my-org", "my-repo")).toBe("gh/my-org/my-repo");
	});

	it("builds slug for bitbucket projects", () => {
		expect(buildProjectSlug("bitbucket", "my-org", "my-repo")).toBe("bb/my-org/my-repo");
	});

	it("defaults to 'gh' prefix for unknown vcs types", () => {
		expect(buildProjectSlug("gitlab", "my-org", "my-repo")).toBe("gh/my-org/my-repo");
	});

	it("defaults to 'gh' prefix for undefined vcs type", () => {
		expect(buildProjectSlug(undefined, "my-org", "my-repo")).toBe("gh/my-org/my-repo");
	});
});
