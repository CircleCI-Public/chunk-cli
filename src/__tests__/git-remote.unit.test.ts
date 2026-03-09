import { describe, expect, test } from "bun:test";
import { parseGitRemoteUrl } from "../utils/git-remote";

describe("parseGitRemoteUrl", () => {
	test("SSH URL with .git", () => {
		expect(parseGitRemoteUrl("git@github.com:myorg/myrepo.git")).toEqual({
			org: "myorg",
			repo: "myrepo",
		});
	});

	test("HTTPS URL with .git", () => {
		expect(parseGitRemoteUrl("https://github.com/myorg/myrepo.git")).toEqual({
			org: "myorg",
			repo: "myrepo",
		});
	});

	test("HTTPS URL without .git", () => {
		expect(parseGitRemoteUrl("https://github.com/myorg/myrepo")).toEqual({
			org: "myorg",
			repo: "myrepo",
		});
	});

	test("ssh:// protocol URL", () => {
		expect(parseGitRemoteUrl("ssh://git@github.com/myorg/myrepo.git")).toEqual({
			org: "myorg",
			repo: "myrepo",
		});
	});

	test("orgs and repos with hyphens", () => {
		expect(parseGitRemoteUrl("git@github.com:my-org/my-repo.git")).toEqual({
			org: "my-org",
			repo: "my-repo",
		});
	});

	test("orgs and repos with dots", () => {
		expect(parseGitRemoteUrl("https://github.com/my.org/my.repo.git")).toEqual({
			org: "my.org",
			repo: "my.repo",
		});
	});

	test("orgs and repos with underscores", () => {
		expect(parseGitRemoteUrl("git@github.com:my_org/my_repo.git")).toEqual({
			org: "my_org",
			repo: "my_repo",
		});
	});

	test("non-GitHub URL returns null", () => {
		expect(parseGitRemoteUrl("git@gitlab.com:myorg/myrepo.git")).toBeNull();
	});

	test("empty string returns null", () => {
		expect(parseGitRemoteUrl("")).toBeNull();
	});

	test("malformed URL returns null", () => {
		expect(parseGitRemoteUrl("not-a-url")).toBeNull();
	});

	test("GitHub URL with no repo returns null", () => {
		expect(parseGitRemoteUrl("https://github.com/myorg")).toBeNull();
	});
});
