import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DEFAULT_OUTPUT_PATH } from "../../config";
import { hasLegacyOutputPath } from "../../core/build-prompt.steps";

describe("hasLegacyOutputPath", () => {
	let tempDir: string;
	let originalCwd: string;

	beforeEach(() => {
		tempDir = join(
			tmpdir(),
			`chunk-test-legacy-output-${Date.now()}-${Math.random().toString(36).slice(2)}`,
		);
		mkdirSync(tempDir, { recursive: true });
		originalCwd = process.cwd();
		process.chdir(tempDir);
	});

	afterEach(() => {
		process.chdir(originalCwd);
		rmSync(tempDir, { recursive: true, force: true });
	});

	it("returns true when legacy file exists and default output is used", () => {
		writeFileSync(join(tempDir, "review-prompt.md"), "legacy content");

		expect(hasLegacyOutputPath(DEFAULT_OUTPUT_PATH)).toBe(true);
	});

	it("returns false when legacy file does not exist", () => {
		expect(hasLegacyOutputPath(DEFAULT_OUTPUT_PATH)).toBe(false);
	});

	it("returns false when --output is explicitly a non-default path", () => {
		writeFileSync(join(tempDir, "review-prompt.md"), "legacy content");

		expect(hasLegacyOutputPath("custom/output.md")).toBe(false);
	});
});
