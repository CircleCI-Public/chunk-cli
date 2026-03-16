import { afterEach, beforeEach, describe, expect, it, mock } from "bun:test";

// ── Module mocks ───────────────────────────────────────────────────────────────

const mockLoadSecret = mock(async (_name: string): Promise<string | undefined> => undefined);
const mockSaveSecret = mock(async (_name: string, _value: string): Promise<void> => {});
const mockDeleteSecret = mock(async (_name: string): Promise<boolean> => false);

mock.module("../storage/keychain", () => ({
	loadSecret: mockLoadSecret,
	saveSecret: mockSaveSecret,
	deleteSecret: mockDeleteSecret,
}));

const { resolveCircleCIToken, runCircleCIAuthStatus, runCircleCIAuthLogout, CIRCLECI_TOKEN_KEY } =
	await import("../commands/auth-circleci");

// ── resolveCircleCIToken ───────────────────────────────────────────────────────

describe("resolveCircleCIToken", () => {
	const originalEnv = process.env.CIRCLECI_TOKEN;

	afterEach(() => {
		if (originalEnv === undefined) {
			delete process.env.CIRCLECI_TOKEN;
		} else {
			process.env.CIRCLECI_TOKEN = originalEnv;
		}
		mockLoadSecret.mockReset();
	});

	it("returns the env var when CIRCLECI_TOKEN is set, without consulting the keychain", async () => {
		process.env.CIRCLECI_TOKEN = "env-token";

		const result = await resolveCircleCIToken();

		expect(result).toBe("env-token");
		expect(mockLoadSecret).not.toHaveBeenCalled();
	});

	it("returns the keychain value when CIRCLECI_TOKEN is not set", async () => {
		delete process.env.CIRCLECI_TOKEN;
		mockLoadSecret.mockResolvedValueOnce("keychain-token");

		const result = await resolveCircleCIToken();

		expect(result).toBe("keychain-token");
		expect(mockLoadSecret).toHaveBeenCalledWith(CIRCLECI_TOKEN_KEY);
	});

	it("returns undefined when neither env nor keychain has a token", async () => {
		delete process.env.CIRCLECI_TOKEN;
		mockLoadSecret.mockResolvedValueOnce(undefined);

		const result = await resolveCircleCIToken();

		expect(result).toBeUndefined();
	});
});

// ── runCircleCIAuthStatus ──────────────────────────────────────────────────────

describe("runCircleCIAuthStatus", () => {
	const originalEnv = process.env.CIRCLECI_TOKEN;
	const logs: string[] = [];
	const originalLog = console.log;

	beforeEach(() => {
		logs.length = 0;
		console.log = (...args: unknown[]) => logs.push(args.join(" "));
		mockLoadSecret.mockReset();
	});

	afterEach(() => {
		console.log = originalLog;
		if (originalEnv === undefined) {
			delete process.env.CIRCLECI_TOKEN;
		} else {
			process.env.CIRCLECI_TOKEN = originalEnv;
		}
		mockLoadSecret.mockReset();
	});

	it("returns exitCode 0 when neither env nor keychain has a token", async () => {
		delete process.env.CIRCLECI_TOKEN;
		mockLoadSecret.mockResolvedValueOnce(undefined);

		const result = await runCircleCIAuthStatus();

		expect(result.exitCode).toBe(0);
	});

	it("shows env as source when only CIRCLECI_TOKEN env var is set", async () => {
		process.env.CIRCLECI_TOKEN = "env-token";
		mockLoadSecret.mockResolvedValueOnce(undefined);

		await runCircleCIAuthStatus();

		expect(logs.some((l) => l.includes("env (CIRCLECI_TOKEN)"))).toBe(true);
	});

	it("shows keychain as source when only the keychain token exists", async () => {
		delete process.env.CIRCLECI_TOKEN;
		mockLoadSecret.mockResolvedValueOnce("keychain-token");

		await runCircleCIAuthStatus();

		expect(logs.some((l) => l.includes("keychain"))).toBe(true);
	});

	it("shows env as source and a precedence note when both env and keychain have tokens", async () => {
		process.env.CIRCLECI_TOKEN = "env-token";
		mockLoadSecret.mockResolvedValueOnce("keychain-token");

		await runCircleCIAuthStatus();

		expect(logs.some((l) => l.includes("env (CIRCLECI_TOKEN)"))).toBe(true);
		expect(logs.some((l) => l.includes("precedence"))).toBe(true);
	});
});

// ── runCircleCIAuthLogout ──────────────────────────────────────────────────────

describe("runCircleCIAuthLogout", () => {
	afterEach(() => {
		mockDeleteSecret.mockReset();
	});

	it("returns exitCode 0 and deletes the keychain entry when a token exists", async () => {
		mockDeleteSecret.mockResolvedValueOnce(true);

		const result = await runCircleCIAuthLogout();

		expect(result.exitCode).toBe(0);
		expect(mockDeleteSecret).toHaveBeenCalledWith(CIRCLECI_TOKEN_KEY);
	});

	it("returns exitCode 0 even when no keychain token was found", async () => {
		mockDeleteSecret.mockResolvedValueOnce(false);

		const result = await runCircleCIAuthLogout();

		expect(result.exitCode).toBe(0);
	});
});
