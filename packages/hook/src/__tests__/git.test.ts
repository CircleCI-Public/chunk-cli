import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import {
	_resetRunCommand,
	_setRunCommand,
	detectChanges,
	filterByPattern,
	getChangedFiles,
	getChangedPackages,
	hasStagedChanges,
	hasUncommittedChanges,
	substitutePlaceholders,
} from "../lib/git";
import type { RunResult } from "../lib/proc";

// ---------------------------------------------------------------------------
// Intercept runCommand via the module's injectable reference so we never
// shell out to real git and never pollute the shared module registry.
// ---------------------------------------------------------------------------
let mockResult: RunResult = { exitCode: 0, output: "", command: "" };
let lastRunOpts: { command: string; cwd: string; timeout: number } | undefined;
let allRunOpts: { command: string; cwd: string; timeout: number }[] = [];

describe("git helpers", () => {
	beforeEach(() => {
		mockResult = { exitCode: 0, output: "", command: "" };
		lastRunOpts = undefined;
		allRunOpts = [];
		_setRunCommand(async (opts) => {
			lastRunOpts = opts as { command: string; cwd: string; timeout: number };
			allRunOpts.push(opts as { command: string; cwd: string; timeout: number });
			return mockResult;
		});
	});

	afterEach(() => {
		_resetRunCommand();
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
		it("replaces {{CHANGED_FILES}} with shell-quoted paths", () => {
			const result = substitutePlaceholders("go test {{CHANGED_FILES}}", ["a.go", "b.go"]);
			expect(result).toBe("go test 'a.go' 'b.go'");
		});

		it("replaces {{CHANGED_PACKAGES}} with shell-quoted paths", () => {
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

		it("replaces both placeholders with shell-quoted paths", () => {
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

	// -----------------------------------------------------------------------
	// filterByPattern
	// -----------------------------------------------------------------------
	describe("filterByPattern()", () => {
		it("filters files by glob pattern against basename", () => {
			const files = ["src/foo.test.ts", "src/bar.ts", "scripts/build.ts", "lib/baz.test.ts"];
			expect(filterByPattern(files, "*.test.ts")).toEqual(["src/foo.test.ts", "lib/baz.test.ts"]);
		});

		it("returns empty array when nothing matches", () => {
			const files = ["src/bar.ts", "scripts/build.ts"];
			expect(filterByPattern(files, "*.test.ts")).toEqual([]);
		});

		it("returns all files when all match", () => {
			const files = ["a.test.ts", "b.test.ts"];
			expect(filterByPattern(files, "*.test.ts")).toEqual(["a.test.ts", "b.test.ts"]);
		});

		it("matches root-level files", () => {
			expect(filterByPattern(["foo.test.ts"], "*.test.ts")).toEqual(["foo.test.ts"]);
		});

		it("handles empty input", () => {
			expect(filterByPattern([], "*.test.ts")).toEqual([]);
		});

		it("supports *.spec.ts glob pattern", () => {
			const files = ["src/foo.spec.ts", "src/foo.test.ts", "src/utils.ts"];
			expect(filterByPattern(files, "*.spec.ts")).toEqual(["src/foo.spec.ts"]);
		});

		it("supports _test.go suffix pattern", () => {
			const files = ["pkg/handler_test.go", "pkg/handler.go", "main.go"];
			expect(filterByPattern(files, "*_test.go")).toEqual(["pkg/handler_test.go"]);
		});

		it("matches against basename only, ignoring directory names", () => {
			const files = ["test/helpers/utils.ts", "src/__tests__/foo.test.ts"];
			expect(filterByPattern(files, "*.test.ts")).toEqual(["src/__tests__/foo.test.ts"]);
		});

		it("preserves full paths in output", () => {
			const files = ["deeply/nested/dir/foo.test.ts"];
			expect(filterByPattern(files, "*.test.ts")).toEqual(["deeply/nested/dir/foo.test.ts"]);
		});
	});

	// -----------------------------------------------------------------------
	// getChangedFiles with testFilePattern
	// -----------------------------------------------------------------------
	describe("getChangedFiles() with testFilePattern", () => {
		it("applies testFilePattern after fileExt filter", async () => {
			mockResult = {
				exitCode: 0,
				output: "src/foo.test.ts\nsrc/bar.ts\nscripts/build.ts\n",
				command: "",
			};
			const files = await getChangedFiles({ fileExt: ".ts", testFilePattern: "*.test.ts" });
			expect(files).toEqual(["src/foo.test.ts"]);
		});

		it("returns empty when testFilePattern filters out all fileExt matches", async () => {
			mockResult = {
				exitCode: 0,
				output: "src/bar.ts\nscripts/build.ts\n",
				command: "",
			};
			const files = await getChangedFiles({ fileExt: ".ts", testFilePattern: "*.test.ts" });
			expect(files).toEqual([]);
		});

		it("works with testFilePattern alone (no fileExt)", async () => {
			mockResult = {
				exitCode: 0,
				output: "src/foo.test.ts\nREADME.md\n",
				command: "",
			};
			const files = await getChangedFiles({ testFilePattern: "*.test.ts" });
			expect(files).toEqual(["src/foo.test.ts"]);
		});

		it("does not filter when testFilePattern is empty (backward compat)", async () => {
			mockResult = {
				exitCode: 0,
				output: "src/foo.test.ts\nsrc/bar.ts\nscripts/build.ts\n",
				command: "",
			};
			const files = await getChangedFiles({ fileExt: ".ts", testFilePattern: "" });
			expect(files).toEqual(["src/foo.test.ts", "src/bar.ts", "scripts/build.ts"]);
		});

		it("does not filter when testFilePattern is omitted (backward compat)", async () => {
			mockResult = {
				exitCode: 0,
				output: "src/foo.test.ts\nsrc/bar.ts\nscripts/build.ts\n",
				command: "",
			};
			const files = await getChangedFiles({ fileExt: ".ts" });
			expect(files).toEqual(["src/foo.test.ts", "src/bar.ts", "scripts/build.ts"]);
		});

		it("fileExt narrows first, then testFilePattern narrows further", async () => {
			mockResult = {
				exitCode: 0,
				output: "src/foo.test.ts\nsrc/bar.ts\nREADME.md\nscripts/build.ts\nlib/baz.test.ts\n",
				command: "",
			};
			const files = await getChangedFiles({ fileExt: ".ts", testFilePattern: "*.test.ts" });
			// README.md excluded by fileExt, bar.ts and build.ts excluded by testFilePattern
			expect(files).toEqual(["src/foo.test.ts", "lib/baz.test.ts"]);
		});
	});

	// -----------------------------------------------------------------------
	// substitutePlaceholders with testFilePattern-filtered results
	// -----------------------------------------------------------------------
	describe("substitutePlaceholders() with testFilePattern-filtered files", () => {
		it("substitutes only test files into {{CHANGED_FILES}}", () => {
			// Simulates the scenario: fileExt=.ts + testFilePattern=*.test.ts
			// Only test files should appear in the command
			const filteredFiles = ["src/foo.test.ts", "lib/baz.test.ts"];
			const result = substitutePlaceholders("bun test {{CHANGED_FILES}}", filteredFiles);
			expect(result).toBe("bun test 'src/foo.test.ts' 'lib/baz.test.ts'");
		});

		it("substitutes empty string when all files filtered out", () => {
			// When testFilePattern removes all files, {{CHANGED_FILES}} → ""
			const result = substitutePlaceholders("bun test {{CHANGED_FILES}}", []);
			expect(result).toBe("bun test ");
		});
	});
});
