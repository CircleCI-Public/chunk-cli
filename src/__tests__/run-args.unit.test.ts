import { describe, expect, it } from "bun:test";
import { parseArgs } from "../utils/args";

describe("Run Command Argument Parsing", () => {
	describe("Basic run command", () => {
		it("should parse 'chunk run' command", () => {
			const result = parseArgs(["node", "chunk", "run"]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBeUndefined();
		});

		it("should parse 'chunk run init' subcommand", () => {
			const result = parseArgs(["node", "chunk", "run", "init"]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("init");
		});

		it("should parse 'chunk run list' subcommand", () => {
			const result = parseArgs(["node", "chunk", "run", "list"]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("list");
		});

		it("should parse 'chunk run <name>' with name as subcommand", () => {
			const result = parseArgs(["node", "chunk", "run", "dev"]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("dev");
		});

		it("should parse 'chunk run <uuid>' with UUID as subcommand", () => {
			const result = parseArgs(["node", "chunk", "run", "e2016e4e-0172-47b3-a4ea-a3ee1a592dba"]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("e2016e4e-0172-47b3-a4ea-a3ee1a592dba");
		});
	});

	describe("Run command flags", () => {
		it("should parse --prompt flag", () => {
			const result = parseArgs(["node", "chunk", "run", "dev", "--prompt", "Fix the bug"]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("dev");
			expect(result.flags.prompt).toBe("Fix the bug");
		});

		it("should parse --environment flag", () => {
			const result = parseArgs([
				"node",
				"chunk",
				"run",
				"dev",
				"--environment",
				"b3c27e5f-1234-5678-9abc-def012345678",
			]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("dev");
			expect(result.flags.environment).toBe("b3c27e5f-1234-5678-9abc-def012345678");
		});

		it("should parse --branch flag", () => {
			const result = parseArgs(["node", "chunk", "run", "dev", "--branch", "feature-branch"]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("dev");
			expect(result.flags.branch).toBe("feature-branch");
		});

		it("should parse --no-new-branch flag", () => {
			const result = parseArgs(["node", "chunk", "run", "dev", "--no-new-branch"]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("dev");
			expect(result.flags["no-new-branch"]).toBe(true);
		});

		it("should parse --no-pipeline-as-tool flag", () => {
			const result = parseArgs(["node", "chunk", "run", "dev", "--no-pipeline-as-tool"]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("dev");
			expect(result.flags["no-pipeline-as-tool"]).toBe(true);
		});

		it("should parse --trigger-source flag", () => {
			const result = parseArgs([
				"node",
				"chunk",
				"run",
				"dev",
				"--trigger-source",
				"custom-source",
			]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("dev");
			expect(result.flags["trigger-source"]).toBe("custom-source");
		});

		it("should parse --help flag", () => {
			const result = parseArgs(["node", "chunk", "run", "--help"]);

			expect(result.command).toBe("run");
			expect(result.flags.help).toBe(true);
		});
	});

	describe("Multiple flags", () => {
		it("should parse multiple flags together", () => {
			const result = parseArgs([
				"node",
				"chunk",
				"run",
				"dev",
				"--prompt",
				"Fix the bug",
				"--branch",
				"main",
				"--environment",
				"b3c27e5f-1234-5678-9abc-def012345678",
			]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("dev");
			expect(result.flags.prompt).toBe("Fix the bug");
			expect(result.flags.branch).toBe("main");
			expect(result.flags.environment).toBe("b3c27e5f-1234-5678-9abc-def012345678");
		});

		it("should parse all flags together", () => {
			const result = parseArgs([
				"node",
				"chunk",
				"run",
				"dev",
				"--prompt",
				"Fix the bug",
				"--environment",
				"b3c27e5f-1234-5678-9abc-def012345678",
				"--branch",
				"develop",
				"--no-new-branch",
				"--no-pipeline-as-tool",
				"--trigger-source",
				"ci-pipeline",
			]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("dev");
			expect(result.flags.prompt).toBe("Fix the bug");
			expect(result.flags.environment).toBe("b3c27e5f-1234-5678-9abc-def012345678");
			expect(result.flags.branch).toBe("develop");
			expect(result.flags["no-new-branch"]).toBe(true);
			expect(result.flags["no-pipeline-as-tool"]).toBe(true);
			expect(result.flags["trigger-source"]).toBe("ci-pipeline");
		});
	});

	describe("Edge cases", () => {
		it("should handle prompt with spaces", () => {
			const result = parseArgs([
				"node",
				"chunk",
				"run",
				"dev",
				"--prompt",
				"This is a multi word prompt",
			]);

			expect(result.flags.prompt).toBe("This is a multi word prompt");
		});

		it("should handle branch names with special characters", () => {
			const result = parseArgs(["node", "chunk", "run", "dev", "--branch", "feature/my-feature"]);

			expect(result.flags.branch).toBe("feature/my-feature");
		});

		it("should handle empty string values", () => {
			const result = parseArgs(["node", "chunk", "run", "dev", "--prompt", ""]);

			expect(result.flags.prompt).toBe("");
		});

		it("should handle flags in different order", () => {
			const result = parseArgs([
				"node",
				"chunk",
				"run",
				"--branch",
				"main",
				"dev",
				"--prompt",
				"task",
			]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("dev");
			expect(result.flags.branch).toBe("main");
			expect(result.flags.prompt).toBe("task");
		});
	});

	describe("Run command with --help", () => {
		it("should parse 'chunk run --help'", () => {
			const result = parseArgs(["node", "chunk", "run", "--help"]);

			expect(result.command).toBe("run");
			expect(result.flags.help).toBe(true);
		});

		it("should parse 'chunk run init --help'", () => {
			const result = parseArgs(["node", "chunk", "run", "init", "--help"]);

			expect(result.command).toBe("run");
			expect(result.subcommand).toBe("init");
			expect(result.flags.help).toBe(true);
		});
	});
});
