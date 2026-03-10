import { describe, expect, it } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";

const SRC_DIR = path.resolve(import.meta.dir, "..");

/**
 * Recursively collect all .ts and .tsx files under a directory.
 */
function collectTsFiles(dir: string): string[] {
	const results: string[] = [];
	for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
		const fullPath = path.join(dir, entry.name);
		if (entry.isDirectory()) {
			results.push(...collectTsFiles(fullPath));
		} else if (entry.isFile() && /\.tsx?$/.test(entry.name)) {
			results.push(fullPath);
		}
	}
	return results;
}

/**
 * Extract import paths from a TypeScript source file.
 * Matches `import ... from "..."` and `import "..."` statements.
 */
function extractImportPaths(filePath: string): string[] {
	const content = fs.readFileSync(filePath, "utf-8");
	const importRegex = /(?:import|export)\s+.*?from\s+["']([^"']+)["']|import\s+["']([^"']+)["']/g;
	const paths: string[] = [];
	let match: RegExpExecArray | null;
	while ((match = importRegex.exec(content)) !== null) {
		const importPath = match[1] ?? match[2];
		if (importPath) paths.push(importPath);
	}
	return paths;
}

/**
 * Check whether a relative import resolves to a file inside a given src subdirectory.
 * E.g., does "../commands/auth" from "src/storage/config.ts" resolve into "src/commands/"?
 */
function importsFromDir(importPath: string, sourceFile: string, targetDir: string): boolean {
	// Only check relative imports
	if (!importPath.startsWith(".")) return false;

	const sourceDir = path.dirname(sourceFile);
	const resolved = path.resolve(sourceDir, importPath);
	const targetAbs = path.resolve(SRC_DIR, targetDir);

	return resolved.startsWith(targetAbs + path.sep) || resolved === targetAbs;
}

/** Directories that must NOT import from commands/ or core/ */
const LEAF_DIRS = ["storage", "api", "review_prompt_mining", "ui", "utils", "skills"];
const FORBIDDEN_TARGETS = ["commands", "core"];

describe("import boundary enforcement", () => {
	for (const leafDir of LEAF_DIRS) {
		const dirPath = path.join(SRC_DIR, leafDir);
		if (!fs.existsSync(dirPath)) continue;

		describe(`src/${leafDir}/`, () => {
			const files = collectTsFiles(dirPath);

			for (const file of files) {
				const relFile = path.relative(SRC_DIR, file);

				it(`${relFile} does not import from commands/ or core/`, () => {
					const imports = extractImportPaths(file);
					const violations: string[] = [];

					for (const imp of imports) {
						for (const target of FORBIDDEN_TARGETS) {
							if (importsFromDir(imp, file, target)) {
								violations.push(`imports "${imp}" (resolves into ${target}/)`);
							}
						}
					}

					expect(violations).toEqual([]);
				});
			}
		});
	}

	describe("src/config/", () => {
		const configDir = path.join(SRC_DIR, "config");
		if (!fs.existsSync(configDir)) return;

		const files = collectTsFiles(configDir);
		/** config/ should not import from any other src/ directory */
		const ALL_SIBLING_DIRS = [
			"commands",
			"core",
			"storage",
			"api",
			"review_prompt_mining",
			"ui",
			"utils",
			"skills",
		];

		for (const file of files) {
			const relFile = path.relative(SRC_DIR, file);

			it(`${relFile} does not import from other src/ directories`, () => {
				const imports = extractImportPaths(file);
				const violations: string[] = [];

				for (const imp of imports) {
					for (const target of ALL_SIBLING_DIRS) {
						if (importsFromDir(imp, file, target)) {
							violations.push(`imports "${imp}" (resolves into ${target}/)`);
						}
					}
				}

				expect(violations).toEqual([]);
			});
		}
	});
});
