/**
 * Unit Tests — Skills installation and status
 *
 * HOME must be set before importing core/skills.ts because AGENTS is a
 * module-level constant resolved at load time. We use a dynamic import after
 * setting HOME so all tests operate against a temporary directory.
 */

import { afterAll, afterEach, describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const originalHome = process.env.HOME;
const testHome = mkdtempSync(join(tmpdir(), "chunk-skills-test-"));
process.env.HOME = testHome;

// Dynamic import AFTER HOME is set — AGENTS resolves to testHome paths.
const { installSkills, getSkillsStatus, listSkills } = await import("../core/skills.ts");

const claudeDir = join(testHome, ".claude");
const codexDir = join(testHome, ".codex");

afterEach(() => {
	rmSync(claudeDir, { recursive: true, force: true });
	rmSync(codexDir, { recursive: true, force: true });
});

afterAll(() => {
	rmSync(testHome, { recursive: true, force: true });
	if (originalHome !== undefined) process.env.HOME = originalHome;
});

// ---------------------------------------------------------------------------
// listSkills
// ---------------------------------------------------------------------------

describe("listSkills", () => {
	it("returns at least one skill", () => {
		expect(listSkills().length).toBeGreaterThan(0);
	});

	it("each entry has non-empty name and description strings", () => {
		for (const skill of listSkills()) {
			expect(typeof skill.name).toBe("string");
			expect(skill.name.length).toBeGreaterThan(0);
			expect(typeof skill.description).toBe("string");
			expect(skill.description.length).toBeGreaterThan(0);
		}
	});

	it("does not expose skill file content", () => {
		for (const skill of listSkills()) {
			expect(Object.keys(skill)).toEqual(["name", "description"]);
		}
	});
});

// ---------------------------------------------------------------------------
// installSkills
// ---------------------------------------------------------------------------

describe("installSkills", () => {
	it("skips all agents when no config dirs exist", () => {
		for (const result of installSkills()) {
			expect(result.skipped).toBe(true);
			expect(result.installed).toEqual([]);
			expect(result.updated).toEqual([]);
		}
	});

	it("installs skills for an agent whose config dir exists", () => {
		mkdirSync(claudeDir, { recursive: true });
		const results = installSkills();
		const claude = results.find((r) => r.agent === "claude");

		expect(claude?.skipped).toBe(false);
		expect(claude?.installed.length).toBeGreaterThan(0);
		expect(claude?.updated).toEqual([]);
	});

	it("writes a SKILL.md for each skill under the agent's skills dir", () => {
		mkdirSync(claudeDir, { recursive: true });
		installSkills();

		for (const { name } of listSkills()) {
			expect(existsSync(join(claudeDir, "skills", name, "SKILL.md"))).toBe(true);
		}
	});

	it("is idempotent — re-running when skills are current reports no changes", () => {
		mkdirSync(claudeDir, { recursive: true });
		installSkills();

		const second = installSkills();
		const claude = second.find((r) => r.agent === "claude");
		expect(claude?.skipped).toBe(false);
		expect(claude?.installed).toEqual([]);
		expect(claude?.updated).toEqual([]);
	});

	it("updates a skill whose content has diverged from the embedded version", () => {
		mkdirSync(claudeDir, { recursive: true });
		installSkills();

		const skillName = listSkills()[0]?.name ?? "";
		writeFileSync(join(claudeDir, "skills", skillName, "SKILL.md"), "stale content", "utf-8");

		const results = installSkills();
		const claude = results.find((r) => r.agent === "claude");
		expect(claude?.updated).toContain(skillName);
		expect(claude?.installed).toEqual([]);
	});

	it("only installs for agents whose config dirs exist — codex skipped when absent", () => {
		mkdirSync(claudeDir, { recursive: true });
		const results = installSkills();

		const claude = results.find((r) => r.agent === "claude");
		const codex = results.find((r) => r.agent === "codex");
		expect(claude?.skipped).toBe(false);
		expect(codex?.skipped).toBe(true);
	});
});

// ---------------------------------------------------------------------------
// getSkillsStatus
// ---------------------------------------------------------------------------

describe("getSkillsStatus", () => {
	it("marks all agents as unavailable when no config dirs exist", () => {
		for (const status of getSkillsStatus()) {
			expect(status.available).toBe(false);
		}
	});

	it("marks an agent as available when its config dir exists", () => {
		mkdirSync(claudeDir, { recursive: true });
		const claude = getSkillsStatus().find((s) => s.agent === "claude");
		expect(claude?.available).toBe(true);
	});

	it("reports all skills as missing before install", () => {
		mkdirSync(claudeDir, { recursive: true });
		const claude = getSkillsStatus().find((s) => s.agent === "claude");
		for (const skill of claude?.skills ?? []) {
			expect(skill.state).toBe("missing");
		}
	});

	it("reports all skills as current after install", () => {
		mkdirSync(claudeDir, { recursive: true });
		installSkills();
		const claude = getSkillsStatus().find((s) => s.agent === "claude");
		for (const skill of claude?.skills ?? []) {
			expect(skill.state).toBe("current");
		}
	});

	it("reports a skill as outdated when its file content has been modified", () => {
		mkdirSync(claudeDir, { recursive: true });
		installSkills();

		const skillName = listSkills()[0]?.name ?? "";
		writeFileSync(join(claudeDir, "skills", skillName, "SKILL.md"), "stale", "utf-8");

		const claude = getSkillsStatus().find((s) => s.agent === "claude");
		const skill = claude?.skills.find((s) => s.name === skillName);
		expect(skill?.state).toBe("outdated");
	});

	it("includes name and description on each skill entry", () => {
		mkdirSync(claudeDir, { recursive: true });
		const claude = getSkillsStatus().find((s) => s.agent === "claude");
		for (const skill of claude?.skills ?? []) {
			expect(typeof skill.name).toBe("string");
			expect(typeof skill.description).toBe("string");
		}
	});
});
