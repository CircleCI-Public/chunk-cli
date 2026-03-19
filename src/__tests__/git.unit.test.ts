import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { generatePatch, getCurrentBranch, resolveRemoteBase } from "../utils/git";

const GIT_ENV = {
	...process.env,
	GIT_AUTHOR_NAME: "Test",
	GIT_AUTHOR_EMAIL: "test@test.com",
	GIT_COMMITTER_NAME: "Test",
	GIT_COMMITTER_EMAIL: "test@test.com",
};

function gitSync(cwd: string, args: string[]): string {
	const result = Bun.spawnSync(["git", ...args], {
		cwd,
		stdout: "pipe",
		stderr: "inherit",
		env: GIT_ENV,
	});
	if (result.exitCode !== 0) {
		throw new Error(`git ${args.join(" ")} failed with exit code ${result.exitCode}`);
	}
	return new TextDecoder().decode(result.stdout).trim();
}

function makeRepo(): string {
	const dir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-git-test-"));
	gitSync(dir, ["init"]);
	gitSync(dir, ["config", "user.email", "test@test.com"]);
	gitSync(dir, ["config", "user.name", "Test"]);
	fs.writeFileSync(path.join(dir, "file.txt"), "content\n");
	gitSync(dir, ["add", "file.txt"]);
	gitSync(dir, ["commit", "-m", "initial"]);
	return dir;
}

describe("getCurrentBranch", () => {
	let repoDir: string;

	beforeEach(() => {
		repoDir = makeRepo();
	});

	afterEach(() => {
		fs.rmSync(repoDir, { recursive: true, force: true });
	});

	it("returns the current branch name", async () => {
		gitSync(repoDir, ["checkout", "-b", "my-feature"]);
		expect(await getCurrentBranch(repoDir)).toBe("my-feature");
	});

	it("returns null in detached HEAD state", async () => {
		const sha = gitSync(repoDir, ["rev-parse", "HEAD"]);
		gitSync(repoDir, ["checkout", sha]);
		expect(await getCurrentBranch(repoDir)).toBeNull();
	});
});

describe("resolveRemoteBase", () => {
	let repoDir: string;

	afterEach(() => {
		fs.rmSync(repoDir, { recursive: true, force: true });
	});

	it("returns null when no remote is configured", async () => {
		repoDir = makeRepo();
		expect(await resolveRemoteBase(repoDir)).toBeNull();
	});

	it("returns origin-head when origin is configured but no upstream tracking branch is set", async () => {
		const originDir = makeRepo();
		repoDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-git-test-"));
		gitSync(repoDir, ["clone", originDir, "."]);
		// Local branch with no upstream — merge-base path is skipped, origin/HEAD fallback fires
		gitSync(repoDir, ["checkout", "-b", "local-only"]);
		const result = await resolveRemoteBase(repoDir);
		expect(result).not.toBeNull();
		expect(result?.type).toBe("origin-head");
		expect(result?.sha).toMatch(/^[0-9a-f]{40}$/);
		fs.rmSync(originDir, { recursive: true, force: true });
	});

	it("returns merge-base when upstream tracking branch is set", async () => {
		const originDir = makeRepo();
		repoDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-git-test-"));
		gitSync(repoDir, ["clone", originDir, "."]);
		gitSync(repoDir, ["checkout", "-b", "feature"]);
		fs.writeFileSync(path.join(repoDir, "feature.txt"), "new\n");
		gitSync(repoDir, ["add", "feature.txt"]);
		gitSync(repoDir, ["commit", "-m", "feature commit"]);
		gitSync(repoDir, ["push", "--set-upstream", "origin", "feature"]);
		const result = await resolveRemoteBase(repoDir);
		expect(result?.type).toBe("merge-base");
		expect(result?.sha).toMatch(/^[0-9a-f]{40}$/);
		fs.rmSync(originDir, { recursive: true, force: true });
	});
});

describe("generatePatch", () => {
	let repoDir: string;

	beforeEach(() => {
		repoDir = makeRepo();
	});

	afterEach(() => {
		fs.rmSync(repoDir, { recursive: true, force: true });
	});

	it("returns empty string when there are no local changes", async () => {
		expect(await generatePatch(repoDir, "HEAD")).toBe("");
	});

	it("returns a patch when a tracked file is modified", async () => {
		fs.writeFileSync(path.join(repoDir, "file.txt"), "modified\n");
		const patch = await generatePatch(repoDir, "HEAD");
		expect(patch).toContain("diff --git");
		expect(patch).toContain("modified");
	});

	it("returns a patch for an untracked file", async () => {
		fs.writeFileSync(path.join(repoDir, "new.txt"), "new content\n");
		const patch = await generatePatch(repoDir, "HEAD");
		expect(patch).toContain("diff --git");
		expect(patch).toContain("new content");
	});

	it("does not leave untracked files staged after generating", async () => {
		fs.writeFileSync(path.join(repoDir, "untracked.txt"), "hello\n");
		await generatePatch(repoDir, "HEAD");
		const staged = gitSync(repoDir, ["diff", "--cached", "--name-only"]);
		expect(staged).not.toContain("untracked.txt");
	});

	it("includes both committed and uncommitted changes relative to the base", async () => {
		// Commit a change, then make an uncommitted change on top
		fs.writeFileSync(path.join(repoDir, "file.txt"), "committed\n");
		gitSync(repoDir, ["add", "file.txt"]);
		gitSync(repoDir, ["commit", "-m", "second"]);
		const base = gitSync(repoDir, ["rev-parse", "HEAD~1"]);
		fs.writeFileSync(path.join(repoDir, "file.txt"), "also uncommitted\n");
		const patch = await generatePatch(repoDir, base);
		expect(patch).toContain("also uncommitted");
	});
});
