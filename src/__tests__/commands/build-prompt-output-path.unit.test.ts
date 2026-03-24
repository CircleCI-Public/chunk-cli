import { afterEach, beforeEach, describe, expect, it, mock } from "bun:test";
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DEFAULT_OUTPUT_PATH, LEGACY_OUTPUT_PATH } from "../../config";
import { warnIfLegacyOutputPath } from "../../core/build-prompt.steps";

describe("warnIfLegacyOutputPath", () => {
	let tempDir: string;
	let originalCwd: string;
	let originalConsoleWarn: typeof console.warn;
	let consoleSpy: ReturnType<typeof mock>;

	beforeEach(() => {
		tempDir = join(
			tmpdir(),
			`chunk-test-legacy-output-${Date.now()}-${Math.random().toString(36).slice(2)}`,
		);
		mkdirSync(tempDir, { recursive: true });
		originalCwd = process.cwd();
		originalConsoleWarn = console.warn;
		process.chdir(tempDir);
		consoleSpy = mock(() => {});
		console.warn = consoleSpy;
	});

	afterEach(() => {
		process.chdir(originalCwd);
		rmSync(tempDir, { recursive: true, force: true });
		console.warn = originalConsoleWarn;
	});

	it("prints a deprecation warning when legacy file exists and default output is used", () => {
		// Create the legacy output file in the temp directory
		writeFileSync(join(tempDir, "review-prompt.md"), "legacy content");

		const result = warnIfLegacyOutputPath(DEFAULT_OUTPUT_PATH);

		expect(result).toBe(true);
		expect(consoleSpy).toHaveBeenCalled();
		const allArgs = consoleSpy.mock.calls.map((c: unknown[]) => String(c[0])).join("\n");
		expect(allArgs).toContain("[deprecation]");
		expect(allArgs).toContain(LEGACY_OUTPUT_PATH);
	});

	it("does NOT print a warning when legacy file does not exist", () => {
		// No legacy file created

		const result = warnIfLegacyOutputPath(DEFAULT_OUTPUT_PATH);

		expect(result).toBe(false);
		expect(consoleSpy).not.toHaveBeenCalled();
	});

	it("does NOT print a warning when --output is explicitly a non-default path", () => {
		// Create the legacy output file
		writeFileSync(join(tempDir, "review-prompt.md"), "legacy content");

		const customOutput = "custom/output.md";
		const result = warnIfLegacyOutputPath(customOutput);

		expect(result).toBe(false);
		expect(consoleSpy).not.toHaveBeenCalled();
	});
});
