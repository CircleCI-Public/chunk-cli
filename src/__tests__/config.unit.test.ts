import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";

import { getConfigFile, getLegacyConfigFile, getUserConfigDir } from "../config";
import { loadUserConfig } from "../storage/config";

describe("config paths", () => {
	const saved: Record<string, string | undefined> = {};

	function setEnv(key: string, val: string | undefined) {
		saved[key] = process.env[key];
		if (val === undefined) delete process.env[key];
		else process.env[key] = val;
	}

	afterEach(() => {
		for (const [k, v] of Object.entries(saved)) {
			if (v === undefined) delete process.env[k];
			else process.env[k] = v;
		}
	});

	it("getUserConfigDir defaults to ~/.config/chunk", () => {
		setEnv("XDG_CONFIG_HOME", undefined);
		const dir = getUserConfigDir();
		expect(dir).toBe(join(homedir(), ".config", "chunk"));
	});

	it("getUserConfigDir respects XDG_CONFIG_HOME", () => {
		setEnv("XDG_CONFIG_HOME", "/custom/xdg");
		expect(getUserConfigDir()).toBe("/custom/xdg/chunk");
	});

	it("getConfigFile is under getUserConfigDir", () => {
		setEnv("XDG_CONFIG_HOME", "/custom/xdg");
		expect(getConfigFile()).toBe("/custom/xdg/chunk/config.json");
	});

	it("getLegacyConfigFile points to ~/.chunk/config.json", () => {
		const legacy = getLegacyConfigFile();
		expect(legacy).toBe(join(homedir(), ".chunk", "config.json"));
	});
});

describe("config migration", () => {
	const testDir = join(tmpdir(), "chunk-config-migration-test", String(Date.now()));
	const saved: Record<string, string | undefined> = {};

	function setEnv(key: string, val: string | undefined) {
		saved[key] = process.env[key];
		if (val === undefined) delete process.env[key];
		else process.env[key] = val;
	}

	beforeEach(() => {
		mkdirSync(testDir, { recursive: true });
	});

	afterEach(() => {
		rmSync(testDir, { recursive: true, force: true });
		for (const [k, v] of Object.entries(saved)) {
			if (v === undefined) delete process.env[k];
			else process.env[k] = v;
		}
	});

	it("migrates legacy config to new XDG location", () => {
		const xdgDir = join(testDir, "xdg");
		setEnv("XDG_CONFIG_HOME", xdgDir);

		// Create legacy file inside the test directory (not real home)
		const legacyDir = join(testDir, "legacy-home", ".chunk");
		const legacyFile = join(legacyDir, "config.json");
		mkdirSync(legacyDir, { recursive: true });
		writeFileSync(legacyFile, '{"apiKey":"test-migrate-key"}');

		const config = loadUserConfig(legacyFile);
		expect(config.apiKey).toBe("test-migrate-key");

		const newPath = join(xdgDir, "chunk", "config.json");
		expect(existsSync(newPath)).toBe(true);

		// Legacy should still exist (copy, not move)
		expect(existsSync(legacyFile)).toBe(true);
	});

	it("skips migration when new config already exists", () => {
		const xdgDir = join(testDir, "xdg");
		const newDir = join(xdgDir, "chunk");
		mkdirSync(newDir, { recursive: true });
		writeFileSync(join(newDir, "config.json"), '{"apiKey":"existing-key"}');

		setEnv("XDG_CONFIG_HOME", xdgDir);

		const config = loadUserConfig();
		expect(config.apiKey).toBe("existing-key");
	});

	it("falls back to legacy config when new path is not writable", () => {
		// Point XDG to a non-writable path so migration fails
		setEnv("XDG_CONFIG_HOME", join("/dev/null", "bad-path"));

		const legacyDir = join(testDir, "legacy-home", ".chunk");
		const legacyFile = join(legacyDir, "config.json");
		mkdirSync(legacyDir, { recursive: true });
		writeFileSync(legacyFile, '{"apiKey":"fallback-key"}');

		const config = loadUserConfig(legacyFile);
		expect(config.apiKey).toBe("fallback-key");
	});

	it("returns empty config when neither legacy nor new exists", () => {
		const xdgDir = join(testDir, "empty-xdg");
		mkdirSync(xdgDir, { recursive: true });
		setEnv("XDG_CONFIG_HOME", xdgDir);

		// Pass a non-existent legacy path so it doesn't read real home
		const fakeLegacy = join(testDir, "no-such-legacy", "config.json");
		const config = loadUserConfig(fakeLegacy);
		expect(config.apiKey).toBeUndefined();
	});
});
