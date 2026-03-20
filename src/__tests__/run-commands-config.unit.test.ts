import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";
import {
	configExists,
	listCommands,
	loadRunConfig,
	resolveCommand,
	saveCommand,
} from "../core/run-config";

describe("core/run-config", () => {
	const testDir = path.join(process.cwd(), ".test-commands-config");
	const chunkDir = path.join(testDir, ".chunk");
	const configPath = path.join(chunkDir, "commands.json");

	beforeEach(() => {
		fs.mkdirSync(testDir, { recursive: true });
	});

	afterEach(() => {
		fs.rmSync(testDir, { recursive: true, force: true });
	});

	describe("configExists", () => {
		it("returns false when no config file", () => {
			expect(configExists(testDir)).toBe(false);
		});

		it("returns true when config file exists", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(configPath, "{}");
			expect(configExists(testDir)).toBe(true);
		});
	});

	describe("loadRunConfig", () => {
		it("returns empty object when no file", () => {
			expect(loadRunConfig(testDir)).toEqual({});
		});

		it("parses valid JSON", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(configPath, JSON.stringify({ commands: { test: "npm test" } }));
			const config = loadRunConfig(testDir);
			expect(config.commands?.test).toBe("npm test");
		});

		it("returns empty object for invalid JSON", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(configPath, "not json{");
			expect(loadRunConfig(testDir)).toEqual({});
		});
	});

	describe("resolveCommand", () => {
		it("resolves string shorthand", () => {
			const config = { commands: { test: "npm test" } };
			const resolved = resolveCommand("test", config);
			expect(resolved).toEqual({ run: "npm test", description: "", timeout: 300, fileExt: "" });
		});

		it("resolves expanded form", () => {
			const config = {
				commands: {
					test: { run: "npm test", description: "Run tests", timeout: 300 },
				},
			};
			const resolved = resolveCommand("test", config);
			expect(resolved).toEqual({
				run: "npm test",
				description: "Run tests",
				timeout: 300,
				fileExt: "",
			});
		});

		it("resolves expanded form with fileExt", () => {
			const config = {
				commands: {
					test: { run: "bun test", fileExt: ".ts" },
				},
			};
			const resolved = resolveCommand("test", config);
			expect(resolved).toEqual({
				run: "bun test",
				description: "",
				timeout: 300,
				fileExt: ".ts",
			});
		});

		it("fills defaults for expanded form", () => {
			const config = { commands: { test: { run: "npm test" } } };
			const resolved = resolveCommand("test", config);
			expect(resolved).toEqual({ run: "npm test", description: "", timeout: 300, fileExt: "" });
		});

		it("returns undefined for missing command", () => {
			expect(resolveCommand("missing", { commands: { test: "npm test" } })).toBeUndefined();
		});

		it("returns undefined when no commands", () => {
			expect(resolveCommand("test", {})).toBeUndefined();
		});
	});

	describe("listCommands", () => {
		it("returns empty array when no config", () => {
			expect(listCommands(testDir)).toEqual([]);
		});

		it("lists all commands", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(
				configPath,
				JSON.stringify({
					commands: {
						test: "npm test",
						lint: { run: "npm run lint", description: "Lint code" },
					},
				}),
			);
			const commands = listCommands(testDir);
			expect(commands).toHaveLength(2);
			expect(commands[0]?.name).toBe("test");
			expect(commands[0]?.run).toBe("npm test");
			expect(commands[1]?.name).toBe("lint");
			expect(commands[1]?.description).toBe("Lint code");
		});
	});

	describe("saveCommand", () => {
		it("creates config file and directory", () => {
			saveCommand(testDir, "test", "npm test");
			expect(fs.existsSync(configPath)).toBe(true);
			const content = JSON.parse(fs.readFileSync(configPath, "utf-8"));
			expect(content.commands.test).toBe("npm test");
		});

		it("preserves existing commands", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(configPath, JSON.stringify({ commands: { lint: "npm run lint" } }));
			saveCommand(testDir, "test", "npm test");
			const content = JSON.parse(fs.readFileSync(configPath, "utf-8"));
			expect(content.commands.lint).toBe("npm run lint");
			expect(content.commands.test).toBe("npm test");
		});

		it("overwrites existing command", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(configPath, JSON.stringify({ commands: { test: "npm test" } }));
			saveCommand(testDir, "test", "bun test");
			const content = JSON.parse(fs.readFileSync(configPath, "utf-8"));
			expect(content.commands.test).toBe("bun test");
		});
	});
});
