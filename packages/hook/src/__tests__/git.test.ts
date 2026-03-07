import { beforeEach, describe, expect, it, mock } from "bun:test";
import type { RunResult } from "../lib/proc";

// ---------------------------------------------------------------------------
// Mock runCommand so getChangedFiles never shells out to real git.
// ---------------------------------------------------------------------------
let mockResult: RunResult = { exitCode: 0, output: "", command: "" };
let lastRunOpts: { command: string; cwd: string; timeout: number } | undefined;
let allRunOpts: { command: string; cwd: string; timeout: number }[] = [];

mock.module("../lib/proc", () => ({
	runCommand: async (opts: { command: string; cwd: string; timeout: number }) => {
		lastRunOpts = opts;
		allRunOpts.push(opts);
		return mockResult;
	},
}));

// Import after mocking so the mock is in effect.
import {
	detectChanges,
	getChangedFiles,
	getChangedPackages,
	hasStagedChanges,
	hasUncommittedChanges,
	substitutePlaceholders,
} from "../lib/git";

describe("git helpers", () => {
	beforeEach(() => {
		mockResult = { exitCode: 0, output: "", command: "" };
		lastRunOpts = undefined;
		allRunOpts = [];
	});

	// -----------------------------------------------------------------------
	// getChangedFiles
	// -----------------------------------------------------------------------
	describe("getChangedFiles()", () => {
		describe("non-staged path (git status)", () => {
			it("parses basic output lines", async () => {
				mockResult = {
					exitCode: 0,
					output: "src/lib/foo.ts\nsrc/lib/bar.ts\n",
					command: "",
				};
				const files = await getChangedFiles({ stagedOnly: false });
				expect(files).toEqual(["src/lib/foo.ts", "src/lib/bar.ts"]);
			});

			it("returns empty array on non-zero exit code", async () => {
				mockResult = { exitCode: 128, output: "fatal: not a git repo", command: "" };
				const files = await getChangedFiles();
				expect(files).toEqual([]);
			});

			it("returns empty array for empty output", async () => {
				mockResult = { exitCode: 0, output: "", command: "" };
				const files = await getChangedFiles();
				expect(files).toEqual([]);
			});

			it("trims whitespace and skips blank lines", async () => {
				mockResult = { exitCode: 0, output: "  a.ts \n\n  b.ts  \n", command: "" };
				const files = await getChangedFiles();
				expect(files).toEqual(["a.ts", "b.ts"]);
			});

			it("strips surrounding quotes from filenames with special chars", async () => {
				// git status --porcelain quotes filenames containing spaces etc.
				mockResult = {
					exitCode: 0,
					output: '"file with spaces.ts"\nplain.ts\n',
					command: "",
				};
				const files = await getChangedFiles();
				expect(files).toEqual(["file with spaces.ts", "plain.ts"]);
			});
		});

		describe("command selection", () => {
			it("uses git diff --cached for staged path", async () => {
				await getChangedFiles({ stagedOnly: true });
				expect(lastRunOpts?.command).toStartWith(
					"git diff --cached --name-only --diff-filter=ACMR",
				);
			});

			it("uses git status --porcelain for non-staged path", async () => {
				await getChangedFiles({ stagedOnly: false });
				expect(lastRunOpts?.command).toStartWith("git status --porcelain -uall");
			});
		});

		describe("fileExt filtering", () => {
			it("filters by extension with leading dot", async () => {
				mockResult = {
					exitCode: 0,
					output: "a.ts\nb.go\nc.ts\n",
					command: "",
				};
				const files = await getChangedFiles({ fileExt: ".ts" });
				expect(files).toEqual(["a.ts", "c.ts"]);
			});

			it("auto-prepends dot when missing", async () => {
				mockResult = {
					exitCode: 0,
					output: "a.ts\nb.go\nc.ts\n",
					command: "",
				};
				const files = await getChangedFiles({ fileExt: "go" });
				expect(files).toEqual(["b.go"]);
			});

			it("returns empty when no files match extension", async () => {
				mockResult = {
					exitCode: 0,
					output: "a.ts\nb.ts\n",
					command: "",
				};
				const files = await getChangedFiles({ fileExt: ".go" });
				expect(files).toEqual([]);
			});

			it("works on quoted filenames after quote stripping", async () => {
				mockResult = {
					exitCode: 0,
					output: '"spaced file.ts"\nplain.go\n',
					command: "",
				};
				const files = await getChangedFiles({ fileExt: ".ts" });
				expect(files).toEqual(["spaced file.ts"]);
			});
		});
	});

	// -----------------------------------------------------------------------
	// getChangedPackages
	// -----------------------------------------------------------------------
	describe("getChangedPackages()", () => {
		it("deduplicates parent directories", () => {
			const files = ["src/lib/env.ts", "src/lib/config.ts", "src/commands/test.ts", "README.md"];
			const packages = getChangedPackages(files);
			expect(packages).toEqual(["./", "./src/commands", "./src/lib"]);
		});

		it("returns ['./'] for root-level files", () => {
			const packages = getChangedPackages(["file.ts"]);
			expect(packages).toEqual(["./"]);
		});

		it("returns empty array for empty input", () => {
			expect(getChangedPackages([])).toEqual([]);
		});
	});

	describe("substitutePlaceholders()", () => {
		it("replaces {{CHANGED_FILES}}", () => {
			const result = substitutePlaceholders("go test {{CHANGED_FILES}}", ["a.go", "b.go"]);
			expect(result).toBe("go test 'a.go' 'b.go'");
		});

		it("replaces {{CHANGED_PACKAGES}}", () => {
			const result = substitutePlaceholders("go test {{CHANGED_PACKAGES}}/...", [
				"pkg/a/file.go",
				"pkg/b/file.go",
			]);
			expect(result).toBe("go test './pkg/a' './pkg/b'/...");
		});

		it("handles no placeholders", () => {
			const result = substitutePlaceholders("go test ./...", ["a.go"]);
			expect(result).toBe("go test ./...");
		});

		it("replaces both placeholders", () => {
			const result = substitutePlaceholders("test {{CHANGED_FILES}} in {{CHANGED_PACKAGES}}", [
				"src/a.ts",
			]);
			expect(result).toBe("test 'src/a.ts' in './src'");
		});
	});

	// -----------------------------------------------------------------------
	// hasUncommittedChanges
	// -----------------------------------------------------------------------
	describe("hasUncommittedChanges()", () => {
		it("returns true when there is output", async () => {
			mockResult = { exitCode: 0, output: "M  src/file.ts\n", command: "" };
			expect(await hasUncommittedChanges()).toBe(true);
		});

		it("returns false when output is empty", async () => {
			mockResult = { exitCode: 0, output: "", command: "" };
			expect(await hasUncommittedChanges()).toBe(false);
		});

		it("returns false when output is only whitespace", async () => {
			mockResult = { exitCode: 0, output: "  \n  \n", command: "" };
			expect(await hasUncommittedChanges()).toBe(false);
		});

		it("detects untracked files (uses git status, not git diff)", async () => {
			// git status --porcelain shows untracked files as "?? filename"
			mockResult = { exitCode: 0, output: "?? new-file.ts\n", command: "" };
			expect(await hasUncommittedChanges()).toBe(true);
		});
	});

	// -----------------------------------------------------------------------
	// hasStagedChanges
	// -----------------------------------------------------------------------
	describe("hasStagedChanges()", () => {
		it("returns true when there is staged output", async () => {
			mockResult = {
				exitCode: 0,
				output: " 1 file changed, 5 insertions(+)\n",
				command: "",
			};
			expect(await hasStagedChanges()).toBe(true);
		});

		it("returns false when output is empty", async () => {
			mockResult = { exitCode: 0, output: "", command: "" };
			expect(await hasStagedChanges()).toBe(false);
		});
	});

	// -----------------------------------------------------------------------
	// detectChanges
	// -----------------------------------------------------------------------
	describe("detectChanges()", () => {
		describe("with fileExt", () => {
			it("returns true when staged files match", async () => {
				mockResult = { exitCode: 0, output: "src/foo.go\n", command: "" };
				expect(await detectChanges({ cwd: "/repo", fileExt: ".go", staged: true })).toBe(true);
			});

			it("returns false when staged has no matching files", async () => {
				mockResult = { exitCode: 0, output: "", command: "" };
				expect(await detectChanges({ cwd: "/repo", fileExt: ".go", staged: true })).toBe(false);
				// Only one call — no fallback
				expect(allRunOpts).toHaveLength(1);
			});

			it("returns true when unstaged files match (no staged flag)", async () => {
				mockResult = { exitCode: 0, output: "src/foo.go\n", command: "" };
				expect(await detectChanges({ cwd: "/repo", fileExt: ".go" })).toBe(true);
			});

			it("returns false when no files match (no staged flag)", async () => {
				mockResult = { exitCode: 0, output: "", command: "" };
				expect(await detectChanges({ cwd: "/repo", fileExt: ".go" })).toBe(false);
				expect(allRunOpts).toHaveLength(1);
			});
		});

		describe("without fileExt", () => {
			it("returns true when staged changes exist", async () => {
				mockResult = { exitCode: 0, output: " 1 file changed\n", command: "" };
				expect(await detectChanges({ cwd: "/repo", staged: true })).toBe(true);
			});

			it("returns false when nothing staged", async () => {
				mockResult = { exitCode: 0, output: "", command: "" };
				expect(await detectChanges({ cwd: "/repo", staged: true })).toBe(false);
				// Only one call — no fallback
				expect(allRunOpts).toHaveLength(1);
			});

			it("checks uncommitted changes when staged is not set", async () => {
				mockResult = { exitCode: 0, output: "M  src/file.ts\n", command: "" };
				expect(await detectChanges({ cwd: "/repo" })).toBe(true);
				expect(allRunOpts).toHaveLength(1);
			});

			it("returns false when no uncommitted changes", async () => {
				mockResult = { exitCode: 0, output: "", command: "" };
				expect(await detectChanges({ cwd: "/repo" })).toBe(false);
			});
		});
	});
});
