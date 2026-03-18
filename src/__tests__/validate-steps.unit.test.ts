import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
	buildTestCommandPrompt,
	detectPackageManager,
	gatherRepoContext,
	isGitRepo,
} from "../core/validate.steps";

let tmpDir: string;

beforeEach(() => {
	tmpDir = mkdtempSync(join(tmpdir(), "chunk-validate-steps-"));
});

afterEach(() => {
	rmSync(tmpDir, { recursive: true, force: true });
});

// ---------------------------------------------------------------------------
// isGitRepo
// ---------------------------------------------------------------------------

describe("isGitRepo", () => {
	it("returns false for a plain directory", () => {
		expect(isGitRepo(tmpDir)).toBe(false);
	});

	it("returns true after git init", () => {
		execFileSync("git", ["init", tmpDir], { stdio: "ignore" });
		expect(isGitRepo(tmpDir)).toBe(true);
	});

	it("returns false for a non-existent path", () => {
		expect(isGitRepo(join(tmpDir, "no-such-dir"))).toBe(false);
	});
});

// ---------------------------------------------------------------------------
// detectPackageManager
// ---------------------------------------------------------------------------

describe("detectPackageManager", () => {
	it("returns null when no lockfile is present", () => {
		expect(detectPackageManager(tmpDir)).toBeNull();
	});

	it.each([
		["pnpm-lock.yaml", "pnpm", "pnpm install"],
		["yarn.lock", "yarn", "yarn install --frozen-lockfile"],
		["bun.lock", "bun", "bun install --frozen-lockfile"],
		["bun.lockb", "bun", "bun install --frozen-lockfile"],
		["package-lock.json", "npm", "npm ci"],
	])("detects %s → %s", (lockfile, name, installCommand) => {
		writeFileSync(join(tmpDir, lockfile), "");
		const pm = detectPackageManager(tmpDir);
		expect(pm).not.toBeNull();
		expect(pm?.name).toBe(name);
		expect(pm?.installCommand).toBe(installCommand);
		expect(pm?.lockfile).toBe(lockfile);
	});

	it("prefers pnpm over yarn when both lockfiles exist", () => {
		writeFileSync(join(tmpDir, "pnpm-lock.yaml"), "");
		writeFileSync(join(tmpDir, "yarn.lock"), "");
		expect(detectPackageManager(tmpDir)?.name).toBe("pnpm");
	});

	it("prefers yarn over bun when both lockfiles exist", () => {
		writeFileSync(join(tmpDir, "yarn.lock"), "");
		writeFileSync(join(tmpDir, "bun.lock"), "");
		expect(detectPackageManager(tmpDir)?.name).toBe("yarn");
	});
});

// ---------------------------------------------------------------------------
// gatherRepoContext
// ---------------------------------------------------------------------------

describe("gatherRepoContext", () => {
	it("includes a root file listing", () => {
		writeFileSync(join(tmpDir, "package.json"), '{"name":"test"}');
		const ctx = gatherRepoContext(tmpDir);
		expect(ctx).toContain("Root files:");
		expect(ctx).toContain("package.json");
	});

	it("includes content of known config files", () => {
		const pkg = '{"name":"my-app","scripts":{"test":"bun test"}}';
		writeFileSync(join(tmpDir, "package.json"), pkg);
		const ctx = gatherRepoContext(tmpDir);
		expect(ctx).toContain("--- package.json ---");
		expect(ctx).toContain("my-app");
	});

	it("silently skips config files that do not exist", () => {
		// No files created — should not throw
		expect(() => gatherRepoContext(tmpDir)).not.toThrow();
	});

	it("silently ignores a non-existent cwd", () => {
		expect(() => gatherRepoContext(join(tmpDir, "no-such-dir"))).not.toThrow();
	});

	it("truncates file content at 4000 characters", () => {
		const big = "x".repeat(5000);
		writeFileSync(join(tmpDir, "package.json"), big);
		const ctx = gatherRepoContext(tmpDir);
		// Content section for package.json should not exceed 4000 chars
		const marker = "--- package.json ---\n";
		const start = ctx.indexOf(marker) + marker.length;
		const snippet = ctx.slice(start);
		expect(snippet.length).toBeLessThanOrEqual(4000);
	});

	it("includes nested config files like .chunk/hook/config.yml", () => {
		const hookDir = join(tmpDir, ".chunk", "hook");
		mkdirSync(hookDir, { recursive: true });
		writeFileSync(join(hookDir, "config.yml"), "test_command: bun test");
		const ctx = gatherRepoContext(tmpDir);
		expect(ctx).toContain("--- .chunk/hook/config.yml ---");
		expect(ctx).toContain("bun test");
	});
});

// ---------------------------------------------------------------------------
// buildTestCommandPrompt
// ---------------------------------------------------------------------------

describe("buildTestCommandPrompt", () => {
	const context = "Root files:\npackage.json";

	it("includes the context in the prompt", () => {
		const prompt = buildTestCommandPrompt(context, null);
		expect(prompt).toContain(context);
	});

	it("instructs the model to output only the command string", () => {
		const prompt = buildTestCommandPrompt(context, null);
		expect(prompt).toContain("output ONLY the shell command");
		expect(prompt).toContain("No explanation, no markdown");
	});

	it("omits package manager section when packageManager is null", () => {
		const prompt = buildTestCommandPrompt(context, null);
		expect(prompt).not.toContain("Detected package manager");
	});

	it("includes package manager details when provided", () => {
		const pm = {
			name: "bun",
			installCommand: "bun install --frozen-lockfile",
			lockfile: "bun.lock",
		};
		const prompt = buildTestCommandPrompt(context, pm);
		expect(prompt).toContain("Detected package manager: bun");
		expect(prompt).toContain("lockfile: bun.lock");
		expect(prompt).toContain("Use bun to run tests");
	});

	it("example command in package manager hint matches the detected manager name", () => {
		const pm = { name: "pnpm", installCommand: "pnpm install", lockfile: "pnpm-lock.yaml" };
		const prompt = buildTestCommandPrompt(context, pm);
		expect(prompt).toContain("`pnpm test`");
	});
});
