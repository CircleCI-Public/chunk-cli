/**
 * Unit Tests — User config storage (storage/config.ts)
 *
 * Tests loadUserConfig, saveUserConfig, clearApiKey, and resolveConfig.
 * HOME is redirected to a temp directory so real config files are never touched.
 */

import { afterEach, describe, expect, it } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

// --- Temp home setup (must happen before the module is imported) ---
const testHome = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-storage-config-test-"));
const _originalHome = process.env.HOME;
process.env.HOME = testHome;

// Dynamic import AFTER HOME is set, so getUserConfigDir() resolves to testHome
const { loadUserConfig, saveUserConfig, clearApiKey, resolveConfig } = await import(
	"../storage/config"
);
const { getUserConfigDir, getConfigFile } = await import("../config");

// ---------------------------------------------------------------------------

afterEach(() => {
	// Wipe the config directory between tests for isolation
	const configDir = getUserConfigDir();
	if (fs.existsSync(configDir)) {
		fs.rmSync(configDir, { recursive: true, force: true });
	}
	// Clean up env vars
	delete process.env.ANTHROPIC_API_KEY;
	delete process.env.CODE_REVIEW_CLI_MODEL;
});

// Restore HOME and clean up temp dir when all tests in this file finish
// (Bun doesn't have a top-level afterAll without a describe, so we rely on
//  afterEach + final cleanup in CI; the temp dir is cleaned on process exit anyway)

describe("loadUserConfig", () => {
	it("returns empty object when config file does not exist", () => {
		const config = loadUserConfig();
		expect(config).toEqual({});
	});

	it("returns apiKey from config file", () => {
		saveUserConfig({ apiKey: "test-key-123" });
		const config = loadUserConfig();
		expect(config.apiKey).toBe("test-key-123");
	});

	it("returns model from config file", () => {
		saveUserConfig({ model: "claude-test-model" });
		const config = loadUserConfig();
		expect(config.model).toBe("claude-test-model");
	});

	it("returns both apiKey and model", () => {
		saveUserConfig({ apiKey: "key-abc", model: "some-model" });
		const config = loadUserConfig();
		expect(config.apiKey).toBe("key-abc");
		expect(config.model).toBe("some-model");
	});

	it("returns empty object for invalid JSON in config file", () => {
		const configDir = getUserConfigDir();
		fs.mkdirSync(configDir, { recursive: true });
		fs.writeFileSync(getConfigFile(), "not-json{{{");
		const config = loadUserConfig();
		expect(config).toEqual({});
	});

	it("returns empty object when apiKey is not a string", () => {
		const configDir = getUserConfigDir();
		fs.mkdirSync(configDir, { recursive: true });
		fs.writeFileSync(getConfigFile(), JSON.stringify({ apiKey: 12345 }));
		const config = loadUserConfig();
		expect(config.apiKey).toBeUndefined();
	});
});

describe("saveUserConfig", () => {
	it("creates the config directory if it does not exist", () => {
		const configDir = getUserConfigDir();
		expect(fs.existsSync(configDir)).toBe(false);
		saveUserConfig({ apiKey: "key" });
		expect(fs.existsSync(configDir)).toBe(true);
	});

	it("writes valid JSON to the config file", () => {
		saveUserConfig({ apiKey: "my-key" });
		const raw = fs.readFileSync(getConfigFile(), "utf-8");
		expect(() => JSON.parse(raw)).not.toThrow();
	});

	it("merges with existing config (does not overwrite unrelated keys)", () => {
		saveUserConfig({ apiKey: "key-1", model: "model-1" });
		saveUserConfig({ apiKey: "key-2" });
		const config = loadUserConfig();
		expect(config.apiKey).toBe("key-2");
		expect(config.model).toBe("model-1");
	});

	it("sets file permissions to 0o600", () => {
		saveUserConfig({ apiKey: "key" });
		const stats = fs.statSync(getConfigFile());
		const permissions = stats.mode & 0o777;
		expect(permissions).toBe(0o600);
	});
});

describe("clearApiKey", () => {
	it("returns false when config file does not exist", () => {
		expect(clearApiKey()).toBe(false);
	});

	it("returns false when config exists but has no apiKey", () => {
		saveUserConfig({ model: "some-model" });
		expect(clearApiKey()).toBe(false);
	});

	it("returns true when apiKey is cleared", () => {
		saveUserConfig({ apiKey: "my-key" });
		expect(clearApiKey()).toBe(true);
	});

	it("removes apiKey from config after clearing", () => {
		saveUserConfig({ apiKey: "my-key" });
		clearApiKey();
		const config = loadUserConfig();
		expect(config.apiKey).toBeUndefined();
	});

	it("preserves other config fields after clearing apiKey", () => {
		saveUserConfig({ apiKey: "my-key", model: "keep-this-model" });
		clearApiKey();
		const config = loadUserConfig();
		expect(config.model).toBe("keep-this-model");
	});

	it("deletes the config file entirely when apiKey is the only key", () => {
		saveUserConfig({ apiKey: "my-key" });
		clearApiKey();
		expect(fs.existsSync(getConfigFile())).toBe(false);
	});
});

describe("resolveConfig", () => {
	it("uses default model when no other source is set", () => {
		const config = resolveConfig();
		expect(config.model).toBeDefined();
		expect(config.sources.model).toBe("default");
	});

	it("resolves apiKey from env var", () => {
		process.env.ANTHROPIC_API_KEY = "env-key";
		const config = resolveConfig();
		expect(config.apiKey).toBe("env-key");
		expect(config.sources.apiKey).toBe("env");
	});

	it("resolves apiKey from flag over env var", () => {
		process.env.ANTHROPIC_API_KEY = "env-key";
		const config = resolveConfig({ flagApiKey: "flag-key" });
		expect(config.apiKey).toBe("flag-key");
		expect(config.sources.apiKey).toBe("flag");
	});

	it("resolves apiKey from user config when env is not set", () => {
		saveUserConfig({ apiKey: "config-key" });
		const config = resolveConfig();
		expect(config.apiKey).toBe("config-key");
		expect(config.sources.apiKey).toBe("config");
	});

	it("flag apiKey takes precedence over user config apiKey", () => {
		saveUserConfig({ apiKey: "config-key" });
		const config = resolveConfig({ flagApiKey: "flag-key" });
		expect(config.apiKey).toBe("flag-key");
		expect(config.sources.apiKey).toBe("flag");
	});

	it("resolves model from env var", () => {
		process.env.CODE_REVIEW_CLI_MODEL = "env-model";
		const config = resolveConfig();
		expect(config.model).toBe("env-model");
		expect(config.sources.model).toBe("env");
	});

	it("resolves model from flag over env var", () => {
		process.env.CODE_REVIEW_CLI_MODEL = "env-model";
		const config = resolveConfig({ flagModel: "flag-model" });
		expect(config.model).toBe("flag-model");
		expect(config.sources.model).toBe("flag");
	});

	it("resolves model from user config when env is not set", () => {
		saveUserConfig({ model: "user-model" });
		const config = resolveConfig();
		expect(config.model).toBe("user-model");
		expect(config.sources.model).toBe("user-config");
	});

	it("returns undefined apiKey when no source provides one", () => {
		const config = resolveConfig();
		expect(config.apiKey).toBeUndefined();
		expect(config.sources.apiKey).toBeUndefined();
	});
});
