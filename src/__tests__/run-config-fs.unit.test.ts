import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";
import {
	getRunConfigPath,
	loadRunConfig,
	type RunConfig,
	saveRunConfig,
} from "../storage/run-config";

describe("Run Config File System Operations", () => {
	// Create a temporary test directory structure
	const testDir = path.join(process.cwd(), ".test-run-config");
	const testGitDir = path.join(testDir, ".git");
	const testChunkDir = path.join(testDir, ".chunk");
	const testConfigPath = path.join(testChunkDir, "run.json");

	const originalCwd = process.cwd();

	beforeEach(() => {
		// Create test directory structure
		if (!fs.existsSync(testDir)) {
			fs.mkdirSync(testDir, { recursive: true });
		}
		if (!fs.existsSync(testGitDir)) {
			fs.mkdirSync(testGitDir, { recursive: true });
		}
		// Change to test directory
		process.chdir(testDir);
	});

	afterEach(() => {
		// Restore original directory
		process.chdir(originalCwd);

		// Clean up test directory
		if (fs.existsSync(testDir)) {
			fs.rmSync(testDir, { recursive: true, force: true });
		}
	});

	describe("getRunConfigPath", () => {
		it("should return path to .chunk/run.json in repo root", () => {
			const configPath = getRunConfigPath();
			expect(configPath).toBe(path.join(testDir, ".chunk", "run.json"));
		});

		it("should find repo root from subdirectory", () => {
			const subDir = path.join(testDir, "src", "commands");
			fs.mkdirSync(subDir, { recursive: true });
			process.chdir(subDir);

			const configPath = getRunConfigPath();
			expect(configPath).toBe(path.join(testDir, ".chunk", "run.json"));
		});

		it("should find .git directory in parent path", () => {
			// Even from a subdirectory, it should still find the git root
			// This test verifies the function walks up the directory tree
			const subDir = path.join(testDir, "deeply", "nested", "directory");
			fs.mkdirSync(subDir, { recursive: true });
			process.chdir(subDir);

			const configPath = getRunConfigPath();
			expect(configPath).toBe(path.join(testDir, ".chunk", "run.json"));
		});
	});

	describe("saveRunConfig", () => {
		it("should create .chunk directory if it doesn't exist", () => {
			const config: RunConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				org_type: "github",
				definitions: {},
			};

			saveRunConfig(config);

			expect(fs.existsSync(testChunkDir)).toBe(true);
		});

		it("should save config to .chunk/run.json", () => {
			const config: RunConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				org_type: "github",
				definitions: {
					dev: {
						definition_id: "e2016e4e-0172-47b3-a4ea-a3ee1a592dba",
						chunk_environment_id: null,
					},
				},
			};

			saveRunConfig(config);

			expect(fs.existsSync(testConfigPath)).toBe(true);
		});

		it("should save valid JSON", () => {
			const config: RunConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				org_type: "github",
				definitions: {
					dev: {
						definition_id: "e2016e4e-0172-47b3-a4ea-a3ee1a592dba",
						chunk_environment_id: null,
					},
				},
			};

			saveRunConfig(config);

			const content = fs.readFileSync(testConfigPath, "utf-8");
			const parsed = JSON.parse(content);

			expect(parsed.org_id).toBe(config.org_id);
			expect(parsed.project_id).toBe(config.project_id);
			expect(parsed.definitions.dev?.definition_id).toBe(config.definitions.dev?.definition_id);
		});

		it("should format JSON with 2-space indentation", () => {
			const config: RunConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				org_type: "github",
				definitions: {},
			};

			saveRunConfig(config);

			const content = fs.readFileSync(testConfigPath, "utf-8");
			expect(content).toContain("  ");
			expect(content.split("\n").length).toBeGreaterThan(1);
		});

		it("should set file permissions to 0o644", () => {
			const config: RunConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				org_type: "github",
				definitions: {},
			};

			saveRunConfig(config);

			const stats = fs.statSync(testConfigPath);
			// Check permissions (mask with 0o777 to get only permission bits)
			const permissions = stats.mode & 0o777;
			expect(permissions).toBe(0o644);
		});
	});

	describe("loadRunConfig", () => {
		it("should load config from .chunk/run.json", () => {
			const config: RunConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				definitions: {
					dev: {
						definition_id: "e2016e4e-0172-47b3-a4ea-a3ee1a592dba",
						chunk_environment_id: null,
					},
				},
			};

			saveRunConfig(config);

			const loaded = loadRunConfig();

			expect(loaded.org_id).toBe(config.org_id);
			expect(loaded.project_id).toBe(config.project_id);
			expect(loaded.definitions.dev?.definition_id).toBe(config.definitions.dev?.definition_id);
		});

		it("should throw error when config file doesn't exist", () => {
			expect(() => loadRunConfig()).toThrow(/Run configuration not found/);
		});

		it("should throw error for invalid JSON", () => {
			fs.mkdirSync(testChunkDir, { recursive: true });
			fs.writeFileSync(testConfigPath, "invalid json{");

			expect(() => loadRunConfig()).toThrow(/Invalid JSON/);
		});

		it("should throw error for config with missing org_id", () => {
			fs.mkdirSync(testChunkDir, { recursive: true });
			fs.writeFileSync(
				testConfigPath,
				JSON.stringify({
					project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
					definitions: {},
				}),
			);

			expect(() => loadRunConfig()).toThrow(/org_id/);
		});

		it("should throw error for config with invalid definition_id UUID", () => {
			fs.mkdirSync(testChunkDir, { recursive: true });
			fs.writeFileSync(
				testConfigPath,
				JSON.stringify({
					org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
					project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
					definitions: {
						dev: {
							definition_id: "not-a-uuid",
						},
					},
				}),
			);

			expect(() => loadRunConfig()).toThrow(/definition_id/);
		});
	});

	describe("Integration: save and load", () => {
		it("should save and load config correctly", () => {
			const config: RunConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				org_type: "github",
				definitions: {
					dev: {
						definition_id: "e2016e4e-0172-47b3-a4ea-a3ee1a592dba",
						chunk_environment_id: "b3c27e5f-1234-5678-9abc-def012345678",
						default_branch: "develop",
						description: "Development environment",
					},
					prod: {
						definition_id: "f3127f5f-0283-48c4-b5fb-b4ff2b693ccb",
						chunk_environment_id: null,
						default_branch: "main",
					},
				},
			};

			saveRunConfig(config);
			const loaded = loadRunConfig();

			expect(loaded).toEqual(config);
		});
	});
});
