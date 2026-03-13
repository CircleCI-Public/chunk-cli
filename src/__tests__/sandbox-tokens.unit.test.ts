import { afterEach, beforeEach, describe, expect, it, mock } from "bun:test";

// ── JWT helpers ────────────────────────────────────────────────────────────────

function makeJWT(payload: Record<string, unknown>): string {
	const header = btoa(JSON.stringify({ alg: "HS256", typ: "JWT" }));
	const body = btoa(JSON.stringify(payload));
	return `${header}.${body}.fakesig`;
}

const nowSec = () => Math.floor(Date.now() / 1000);
const freshJwt = () => makeJWT({ exp: nowSec() + 3600 }); // expires in 1h
const expiredJwt = () => makeJWT({ exp: nowSec() - 3600 }); // expired 1h ago
const soonJwt = () => makeJWT({ exp: nowSec() + 30 }); // expires in 30s (< 60s buffer)
const noExpJwt = () => makeJWT({ sub: "test-user" }); // no exp claim

// ── Module mocks ───────────────────────────────────────────────────────────────

const mockLoadSecret = mock(async (_name: string): Promise<string | undefined> => undefined);
const mockSaveSecret = mock(async (_name: string, _value: string): Promise<void> => {});
const mockDeleteSecret = mock(async (_name: string): Promise<boolean> => false);
const mockCreateSandboxAccessToken = mock(async () => ({ access_token: "new-token" }));

mock.module("../storage/keychain", () => ({
	loadSecret: mockLoadSecret,
	saveSecret: mockSaveSecret,
	deleteSecret: mockDeleteSecret,
}));

mock.module("../api/circleci", () => ({
	createSandboxAccessToken: mockCreateSandboxAccessToken,
}));

const { getSandboxAccessToken, clearSandboxAccessToken } = await import(
	"../storage/sandbox-tokens"
);

// ── Tests ──────────────────────────────────────────────────────────────────────

describe("getSandboxAccessToken", () => {
	const SANDBOX_ID = "sandbox-abc";
	const ORG_ID = "org-xyz";
	const CIRCLECI_TOKEN = "cci-token";

	beforeEach(() => {
		mockLoadSecret.mockReset();
		mockSaveSecret.mockReset();
		mockDeleteSecret.mockReset();
		mockCreateSandboxAccessToken.mockReset();
		mockCreateSandboxAccessToken.mockResolvedValue({ access_token: "new-token" });
	});

	it("returns the cached token without calling the API when it is fresh", async () => {
		const jwt = freshJwt();
		mockLoadSecret.mockResolvedValueOnce(jwt);

		const result = await getSandboxAccessToken(SANDBOX_ID, ORG_ID, CIRCLECI_TOKEN);

		expect(result).toBe(jwt);
		expect(mockCreateSandboxAccessToken).not.toHaveBeenCalled();
	});

	it("fetches a new token and caches it when no cached token exists", async () => {
		mockLoadSecret.mockResolvedValueOnce(undefined);

		const result = await getSandboxAccessToken(SANDBOX_ID, ORG_ID, CIRCLECI_TOKEN);

		expect(result).toBe("new-token");
		expect(mockCreateSandboxAccessToken).toHaveBeenCalledWith(
			SANDBOX_ID,
			ORG_ID,
			CIRCLECI_TOKEN,
		);
		expect(mockSaveSecret).toHaveBeenCalledWith(`sandbox-token-${SANDBOX_ID}`, "new-token");
	});

	it("fetches a new token when the cached token is expired", async () => {
		mockLoadSecret.mockResolvedValueOnce(expiredJwt());

		const result = await getSandboxAccessToken(SANDBOX_ID, ORG_ID, CIRCLECI_TOKEN);

		expect(result).toBe("new-token");
		expect(mockCreateSandboxAccessToken).toHaveBeenCalledTimes(1);
		expect(mockSaveSecret).toHaveBeenCalledTimes(1);
	});

	it("fetches a new token when the cached token expires within the refresh buffer", async () => {
		mockLoadSecret.mockResolvedValueOnce(soonJwt());

		const result = await getSandboxAccessToken(SANDBOX_ID, ORG_ID, CIRCLECI_TOKEN);

		expect(result).toBe("new-token");
		expect(mockCreateSandboxAccessToken).toHaveBeenCalledTimes(1);
	});

	it("returns the cached token when it has no exp claim (treated as always fresh)", async () => {
		const jwt = noExpJwt();
		mockLoadSecret.mockResolvedValueOnce(jwt);

		const result = await getSandboxAccessToken(SANDBOX_ID, ORG_ID, CIRCLECI_TOKEN);

		expect(result).toBe(jwt);
		expect(mockCreateSandboxAccessToken).not.toHaveBeenCalled();
	});

	it("fetches a new token when the cached value is not a valid JWT", async () => {
		mockLoadSecret.mockResolvedValueOnce("not-a-jwt");

		const result = await getSandboxAccessToken(SANDBOX_ID, ORG_ID, CIRCLECI_TOKEN);

		// malformed JWT → getTokenExpiry returns null → isTokenFresh returns true
		// so "not-a-jwt" itself is returned (treated as fresh)
		expect(result).toBe("not-a-jwt");
		expect(mockCreateSandboxAccessToken).not.toHaveBeenCalled();
	});

	it("uses the sandbox-token-<id> key in the keychain", async () => {
		mockLoadSecret.mockResolvedValueOnce(undefined);

		await getSandboxAccessToken(SANDBOX_ID, ORG_ID, CIRCLECI_TOKEN);

		expect(mockLoadSecret).toHaveBeenCalledWith(`sandbox-token-${SANDBOX_ID}`);
		expect(mockSaveSecret).toHaveBeenCalledWith(`sandbox-token-${SANDBOX_ID}`, "new-token");
	});
});

describe("clearSandboxAccessToken", () => {
	it("deletes the sandbox token from the keychain", async () => {
		mockDeleteSecret.mockReset();

		await clearSandboxAccessToken("sandbox-123");

		expect(mockDeleteSecret).toHaveBeenCalledWith("sandbox-token-sandbox-123");
	});
});
