import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";
import {
	configExists,
	listCommands,
	loadRunConfig,
	loadSequenceCommands,
	resolveCommand,
	saveCommand,
	saveCommandsConfig,
} from "../core/run-config";

describe("core/run-config", () => {
	const testDir = path.join(process.cwd(), ".test-commands-config");
	const chunkDir = path.join(testDir, ".chunk");
	const configPath = path.join(chunkDir, "config.json");

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
		it("returns empty config when no file", () => {
			expect(loadRunConfig(testDir)).toEqual({ config: {}, migrated: false });
		});

		it("parses valid array format", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(
				configPath,
				JSON.stringify({ commands: [{ name: "test", run: "npm test" }] }),
			);
			const { config, migrated } = loadRunConfig(testDir);
			expect(migrated).toBe(false);
			expect(config.commands).toHaveLength(1);
			expect(config.commands?.[0]?.name).toBe("test");
			expect(config.commands?.[0]?.run).toBe("npm test");
		});

		it("returns empty config for invalid JSON", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(configPath, "not json{");
			expect(loadRunConfig(testDir)).toEqual({ config: {}, migrated: false });
		});

		it("migrates legacy format (installCommand/testCommand) in place", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(
				configPath,
				JSON.stringify({ installCommand: "npm install", testCommand: "npm test" }),
			);
			const { config, migrated } = loadRunConfig(testDir);
			expect(migrated).toBe(true);
			expect(config.commands).toHaveLength(2);
			expect(config.commands?.[0]).toEqual({ name: "install", run: "npm install" });
			expect(config.commands?.[1]).toEqual({ name: "test", run: "npm test" });
			// Should have rewritten config.json with new format
			const rewritten = JSON.parse(fs.readFileSync(configPath, "utf-8"));
			expect(Array.isArray(rewritten.commands)).toBe(true);
		});

		it("migrates legacy format with only testCommand", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(configPath, JSON.stringify({ testCommand: "bun test" }));
			const { config, migrated } = loadRunConfig(testDir);
			expect(migrated).toBe(true);
			expect(config.commands).toHaveLength(1);
			expect(config.commands?.[0]).toEqual({ name: "test", run: "bun test" });
		});
	});

	describe("resolveCommand", () => {
		it("resolves command by name", () => {
			const config = { commands: [{ name: "test", run: "npm test" }] };
			const resolved = resolveCommand("test", config);
			expect(resolved).toEqual({ run: "npm test", description: "", timeout: 300, fileExt: "" });
		});

		it("fills in optional field defaults", () => {
			const config = {
				commands: [{ name: "test", run: "npm test", description: "Run tests", timeout: 120 }],
			};
			const resolved = resolveCommand("test", config);
			expect(resolved).toEqual({
				run: "npm test",
				description: "Run tests",
				timeout: 120,
				fileExt: "",
			});
		});

		it("resolves fileExt field", () => {
			const config = {
				commands: [{ name: "test", run: "bun test", fileExt: ".ts" }],
			};
			const resolved = resolveCommand("test", config);
			expect(resolved).toEqual({
				run: "bun test",
				description: "",
				timeout: 300,
				fileExt: ".ts",
			});
		});

		it("returns undefined for missing command", () => {
			expect(
				resolveCommand("missing", { commands: [{ name: "test", run: "npm test" }] }),
			).toBeUndefined();
		});

		it("returns undefined when no commands", () => {
			expect(resolveCommand("test", {})).toBeUndefined();
		});
	});

	describe("listCommands", () => {
		it("returns empty array when no config", () => {
			expect(listCommands(testDir)).toEqual([]);
		});

		it("lists commands in array order", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(
				configPath,
				JSON.stringify({
					commands: [
						{ name: "install", run: "npm install" },
						{ name: "test", run: "npm test" },
						{ name: "lint", run: "npm run lint", description: "Lint code" },
					],
				}),
			);
			const commands = listCommands(testDir);
			expect(commands).toHaveLength(3);
			expect(commands[0]?.name).toBe("install");
			expect(commands[1]?.name).toBe("test");
			expect(commands[2]?.name).toBe("lint");
			expect(commands[2]?.description).toBe("Lint code");
		});
	});

	describe("saveCommand", () => {
		it("creates config file with array entry", () => {
			saveCommand(testDir, "test", "npm test");
			expect(fs.existsSync(configPath)).toBe(true);
			const content = JSON.parse(fs.readFileSync(configPath, "utf-8"));
			expect(content.commands).toHaveLength(1);
			expect(content.commands[0]).toEqual({ name: "test", run: "npm test" });
		});

		it("appends new command to array", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(
				configPath,
				JSON.stringify({ commands: [{ name: "lint", run: "npm run lint" }] }),
			);
			saveCommand(testDir, "test", "npm test");
			const content = JSON.parse(fs.readFileSync(configPath, "utf-8"));
			expect(content.commands).toHaveLength(2);
			expect(content.commands[0].name).toBe("lint");
			expect(content.commands[1].name).toBe("test");
		});

		it("updates existing command in place", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(
				configPath,
				JSON.stringify({ commands: [{ name: "test", run: "npm test" }] }),
			);
			saveCommand(testDir, "test", "bun test");
			const content = JSON.parse(fs.readFileSync(configPath, "utf-8"));
			expect(content.commands).toHaveLength(1);
			expect(content.commands[0].run).toBe("bun test");
		});
	});

	describe("loadSequenceCommands", () => {
		it("returns error when no commands", () => {
			const result = loadSequenceCommands(testDir);
			expect("ok" in result).toBe(true);
			if ("ok" in result) expect(result.ok).toBe(false);
		});

		it("returns run strings in array order", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(
				configPath,
				JSON.stringify({
					commands: [
						{ name: "install", run: "bun install" },
						{ name: "test", run: "bun test" },
					],
				}),
			);
			const result = loadSequenceCommands(testDir);
			expect("commands" in result).toBe(true);
			if ("commands" in result) {
				expect(result.commands).toEqual(["bun install", "bun test"]);
			}
		});
	});

	describe("saveCommandsConfig", () => {
		it("writes commands array", () => {
			saveCommandsConfig(testDir, [
				{ name: "install", run: "bun install" },
				{ name: "test", run: "bun test" },
			]);
			const content = JSON.parse(fs.readFileSync(configPath, "utf-8"));
			expect(content.commands).toHaveLength(2);
			expect(content.commands[0]).toEqual({ name: "install", run: "bun install" });
			expect(content.commands[1]).toEqual({ name: "test", run: "bun test" });
		});

		it("preserves existing commands not in the new list", () => {
			fs.mkdirSync(chunkDir, { recursive: true });
			fs.writeFileSync(
				configPath,
				JSON.stringify({ commands: [{ name: "lint", run: "bun run lint" }] }),
			);
			saveCommandsConfig(testDir, [{ name: "test", run: "bun test" }]);
			const content = JSON.parse(fs.readFileSync(configPath, "utf-8"));
			expect(content.commands).toHaveLength(2);
			expect(content.commands[0].name).toBe("test");
			expect(content.commands[1].name).toBe("lint");
		});
	});
});
