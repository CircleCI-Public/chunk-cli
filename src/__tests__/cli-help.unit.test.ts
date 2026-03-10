import { beforeAll, describe, expect, it } from "bun:test";
import { execFileSync } from "node:child_process";
import * as path from "node:path";

const CLI_ENTRY = path.resolve(import.meta.dir, "..", "index.ts");

/**
 * Run the CLI with given arguments and return stdout.
 * Uses `bun run` to execute the TypeScript entry point directly.
 */
function runCli(args: string[]): string {
	const result = execFileSync("bun", ["run", CLI_ENTRY, ...args], {
		encoding: "utf-8",
		timeout: 15_000,
		env: {
			...process.env,
			// Prevent commands from trying to use real tokens
			GITHUB_TOKEN: "",
			ANTHROPIC_API_KEY: "",
			CIRCLECI_TOKEN: "",
		},
	});
	return result;
}

// ──────────────────────────────────────────────────────────────────────────────
// Top-level: `chunk --help`
// ──────────────────────────────────────────────────────────────────────────────
describe("chunk (top-level)", () => {
	let helpOutput: string;

	beforeAll(() => {
		helpOutput = runCli(["--help"]);
	});

	it("prints help without error", () => {
		expect(helpOutput).toBeDefined();
	});

	it("shows the program description", () => {
		expect(helpOutput).toContain("AI code review CLI");
	});

	it("registers the build-prompt command", () => {
		expect(helpOutput).toContain("build-prompt");
	});

	it("registers the auth command", () => {
		expect(helpOutput).toContain("auth");
	});

	it("registers the config command", () => {
		expect(helpOutput).toContain("config");
	});

	it("registers the task command", () => {
		expect(helpOutput).toContain("task");
	});

	it("registers the upgrade command", () => {
		expect(helpOutput).toContain("upgrade");
	});

	it("registers the skills command", () => {
		expect(helpOutput).toContain("skills");
	});

	it("registers the hook command", () => {
		expect(helpOutput).toContain("hook");
	});

	it("shows the version flag", () => {
		expect(helpOutput).toMatch(/-V|--version/);
	});

	it("shows the help flag", () => {
		expect(helpOutput).toMatch(/-h.*--help/);
	});
});

// ──────────────────────────────────────────────────────────────────────────────
// `chunk build-prompt --help`
// ──────────────────────────────────────────────────────────────────────────────
describe("chunk build-prompt --help", () => {
	let helpOutput: string;

	beforeAll(() => {
		helpOutput = runCli(["build-prompt", "--help"]);
	});

	it("prints help without error", () => {
		expect(helpOutput).toBeDefined();
	});

	it("shows --org flag", () => {
		expect(helpOutput).toContain("--org");
	});

	it("shows --repos flag", () => {
		expect(helpOutput).toContain("--repos");
	});

	it("shows --top flag", () => {
		expect(helpOutput).toContain("--top");
	});

	it("shows --since flag", () => {
		expect(helpOutput).toContain("--since");
	});

	it("shows --output flag", () => {
		expect(helpOutput).toContain("--output");
	});

	it("shows --max-comments flag", () => {
		expect(helpOutput).toContain("--max-comments");
	});

	it("shows --analyze-model flag", () => {
		expect(helpOutput).toContain("--analyze-model");
	});

	it("shows --prompt-model flag", () => {
		expect(helpOutput).toContain("--prompt-model");
	});

	it("shows --include-attribution flag", () => {
		expect(helpOutput).toContain("--include-attribution");
	});

	it("mentions GITHUB_TOKEN env var", () => {
		expect(helpOutput).toContain("GITHUB_TOKEN");
	});

	it("mentions ANTHROPIC_API_KEY env var", () => {
		expect(helpOutput).toContain("ANTHROPIC_API_KEY");
	});

	it("describes the org flag", () => {
		expect(helpOutput).toContain("GitHub organization");
	});
});

// ──────────────────────────────────────────────────────────────────────────────
// `chunk task config --help`
// ──────────────────────────────────────────────────────────────────────────────
describe("chunk task config --help", () => {
	let helpOutput: string;

	beforeAll(() => {
		helpOutput = runCli(["task", "config", "--help"]);
	});

	it("prints help without error", () => {
		expect(helpOutput).toBeDefined();
	});

	it("describes the command", () => {
		expect(helpOutput).toContain("run.json");
	});

	it("mentions CIRCLE_TOKEN env var", () => {
		expect(helpOutput).toContain("CIRCLE_TOKEN");
	});
});

// ──────────────────────────────────────────────────────────────────────────────
// `chunk task run --help`
// ──────────────────────────────────────────────────────────────────────────────
describe("chunk task run --help", () => {
	let helpOutput: string;

	beforeAll(() => {
		helpOutput = runCli(["task", "run", "--help"]);
	});

	it("prints help without error", () => {
		expect(helpOutput).toBeDefined();
	});

	it("shows --definition flag as required", () => {
		expect(helpOutput).toContain("--definition");
	});

	it("shows --prompt flag as required", () => {
		expect(helpOutput).toContain("--prompt");
	});

	it("shows --branch flag", () => {
		expect(helpOutput).toContain("--branch");
	});

	it("shows --new-branch flag", () => {
		expect(helpOutput).toContain("--new-branch");
	});

	it("shows --pipeline-as-tool flag", () => {
		expect(helpOutput).toContain("--pipeline-as-tool");
	});

	it("describes the command purpose", () => {
		expect(helpOutput).toContain("Trigger a chunk run");
	});

	it("mentions CIRCLE_TOKEN env var", () => {
		expect(helpOutput).toContain("CIRCLE_TOKEN");
	});

	it("shows usage examples", () => {
		expect(helpOutput).toContain("chunk task run --definition");
	});
});
