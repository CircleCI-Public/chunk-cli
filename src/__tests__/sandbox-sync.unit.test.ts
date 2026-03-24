import { afterAll, afterEach, beforeEach, describe, expect, it, mock } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { syncToSandbox } from "../core/sandboxes";
import { buildSandboxInitCommand } from "../core/sandboxes.steps";
import { generatePatch } from "../utils/git";

// Mock fetch globally — same pattern as circleci-api tests
const mockFetch = mock();
const _originalFetch = global.fetch;
// @ts-expect-error - mock doesn't fully implement fetch type
global.fetch = mockFetch;

afterAll(() => {
	global.fetch = _originalFetch;
});

const ORG_ID = "org-abc";
const SANDBOX_ID = "sandbox-xyz";

describe("buildSandboxInitCommand", () => {
	it("clones with --branch when branch is provided, with plain-clone fallback", () => {
		const cmd = buildSandboxInitCommand(
			"https://github.com/org/repo.git",
			"my-branch",
			"/workspace",
		);
		expect(cmd).toBe(
			"git clone --branch 'my-branch' 'https://github.com/org/repo.git' '/workspace' || git clone 'https://github.com/org/repo.git' '/workspace'",
		);
	});

	it("clones only when branch is null", () => {
		const cmd = buildSandboxInitCommand("https://github.com/org/repo.git", null, "/workspace");
		expect(cmd).toBe("git clone 'https://github.com/org/repo.git' '/workspace'");
		expect(cmd).not.toContain("checkout");
	});
});

describe("generatePatch", () => {
	let repoDir: string;

	function gitSync(args: string[]): void {
		const result = Bun.spawnSync(["git", ...args], {
			cwd: repoDir,
			stdout: "inherit",
			stderr: "inherit",
			env: {
				...process.env,
				GIT_AUTHOR_NAME: "Test",
				GIT_AUTHOR_EMAIL: "test@test.com",
				GIT_COMMITTER_NAME: "Test",
				GIT_COMMITTER_EMAIL: "test@test.com",
			},
		});
		if (result.exitCode !== 0) {
			throw new Error(`git ${args.join(" ")} failed with exit code ${result.exitCode}`);
		}
	}

	beforeEach(() => {
		repoDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-patch-test-"));
		gitSync(["init"]);
		gitSync(["config", "user.email", "test@test.com"]);
		gitSync(["config", "user.name", "Test"]);
		fs.writeFileSync(path.join(repoDir, "file.txt"), "initial content\n");
		gitSync(["add", "file.txt"]);
		gitSync(["commit", "-m", "initial"]);
	});

	afterEach(() => {
		fs.rmSync(repoDir, { recursive: true, force: true });
	});

	it("returns empty string when there are no local changes", async () => {
		const patch = await generatePatch(repoDir, "HEAD");
		expect(patch).toBe("");
	});

	it("returns a patch when a tracked file is modified", async () => {
		fs.writeFileSync(path.join(repoDir, "file.txt"), "modified content\n");
		const patch = await generatePatch(repoDir, "HEAD");
		expect(patch).toContain("diff --git");
		expect(patch).toContain("modified content");
	});

	it("returns a patch for an untracked file", async () => {
		fs.writeFileSync(path.join(repoDir, "new-file.txt"), "new content\n");
		const patch = await generatePatch(repoDir, "HEAD");
		expect(patch).toContain("diff --git");
		expect(patch).toContain("new content");
	});

	it("does not leave untracked files staged in the index after generating", async () => {
		fs.writeFileSync(path.join(repoDir, "untracked.txt"), "hello\n");
		await generatePatch(repoDir, "HEAD");
		const result = Bun.spawnSync(["git", "diff", "--cached", "--name-only"], {
			cwd: repoDir,
			stdout: "pipe",
			stderr: "inherit",
		});
		const staged = new TextDecoder().decode(result.stdout);
		expect(staged).not.toContain("untracked.txt");
	});
});

describe("syncToSandbox", () => {
	const savedToken = process.env.CIRCLE_TOKEN;
	const savedFallbackToken = process.env.CIRCLECI_TOKEN;
	let keyDir: string;
	let identityFile: string;

	beforeEach(() => {
		process.env.CIRCLE_TOKEN = "test-token";
		delete process.env.CIRCLECI_TOKEN;
		mockFetch.mockReset();
		keyDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-key-test-"));
		identityFile = path.join(keyDir, "test_key");
		fs.writeFileSync(identityFile, "fake-private-key");
		fs.writeFileSync(`${identityFile}.pub`, "ssh-ed25519 AAAA fake-public-key test@test.com");
	});

	afterEach(() => {
		fs.rmSync(keyDir, { recursive: true, force: true });
		if (savedToken === undefined) {
			delete process.env.CIRCLE_TOKEN;
		} else {
			process.env.CIRCLE_TOKEN = savedToken;
		}
		if (savedFallbackToken === undefined) {
			delete process.env.CIRCLECI_TOKEN;
		} else {
			process.env.CIRCLECI_TOKEN = savedFallbackToken;
		}
	});

	it("returns exitCode 2 when no token is set", async () => {
		delete process.env.CIRCLE_TOKEN;
		delete process.env.CIRCLECI_TOKEN;
		const result = await syncToSandbox(ORG_ID, SANDBOX_ID, "/workspace");
		expect(result.exitCode).toBe(2);
		expect(mockFetch).not.toHaveBeenCalled();
	});

	it("returns exitCode 2 when addSandboxSshKey fails with 401", async () => {
		mockFetch.mockImplementationOnce(async () => ({
			ok: false,
			status: 401,
			text: async () => "Unauthorized",
		}));
		const result = await syncToSandbox(ORG_ID, SANDBOX_ID, "/workspace", identityFile);
		expect(result.exitCode).toBe(2);
	});

	it("returns exitCode 2 when addSandboxSshKey fails with 403", async () => {
		mockFetch.mockImplementationOnce(async () => ({
			ok: false,
			status: 403,
			text: async () => "Forbidden",
		}));
		const result = await syncToSandbox(ORG_ID, SANDBOX_ID, "/workspace", identityFile);
		expect(result.exitCode).toBe(2);
	});
});
