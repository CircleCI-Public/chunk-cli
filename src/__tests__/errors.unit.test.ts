/**
 * Unit Tests — Error formatting and classification utilities
 */

import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { setColorEnabled } from "../ui/colors";
import { formatError, handleError, isAuthError, isNetworkError } from "../utils/errors";

describe("formatError", () => {
	beforeEach(() => setColorEnabled(false));
	afterEach(() => setColorEnabled(true));

	it("formats brief only", () => {
		const result = formatError("something went wrong");
		expect(result).toContain("✗ Error: something went wrong");
	});

	it("includes detail when provided", () => {
		const result = formatError("brief", "some detail");
		expect(result).toContain("some detail");
	});

	it("includes suggestion when provided", () => {
		const result = formatError("brief", undefined, "try again");
		expect(result).toContain("Suggestion: try again");
	});

	it("includes all three parts when all are provided", () => {
		const result = formatError("brief", "detail text", "do this");
		expect(result).toContain("✗ Error: brief");
		expect(result).toContain("detail text");
		expect(result).toContain("Suggestion: do this");
	});

	it("does not include Suggestion line when suggestion is omitted", () => {
		const result = formatError("brief", "detail");
		expect(result).not.toContain("Suggestion:");
	});

	it("does not include detail when omitted", () => {
		const result = formatError("brief", undefined, "suggestion");
		expect(result).not.toContain("undefined");
	});
});

describe("isNetworkError", () => {
	it("returns true for 'network' in message", () => {
		expect(isNetworkError(new Error("network failure"))).toBe(true);
	});

	it("returns true for 'fetch failed'", () => {
		expect(isNetworkError(new Error("fetch failed"))).toBe(true);
	});

	it("returns true for 'econnrefused'", () => {
		expect(isNetworkError(new Error("ECONNREFUSED connection refused"))).toBe(true);
	});

	it("returns true for 'econnreset'", () => {
		expect(isNetworkError(new Error("ECONNRESET"))).toBe(true);
	});

	it("returns true for 'etimedout'", () => {
		expect(isNetworkError(new Error("ETIMEDOUT connection timed out"))).toBe(true);
	});

	it("returns true for 'enotfound'", () => {
		expect(isNetworkError(new Error("ENOTFOUND host not found"))).toBe(true);
	});

	it("returns true for 'unable to connect'", () => {
		expect(isNetworkError(new Error("Unable to connect to server"))).toBe(true);
	});

	it("returns true for 'internet'", () => {
		expect(isNetworkError(new Error("no internet access"))).toBe(true);
	});

	it("returns true for 'socket hang up'", () => {
		expect(isNetworkError(new Error("socket hang up"))).toBe(true);
	});

	it("returns true for 'failed to fetch'", () => {
		expect(isNetworkError(new Error("Failed to fetch resource"))).toBe(true);
	});

	it("returns false for unrelated errors", () => {
		expect(isNetworkError(new Error("file not found"))).toBe(false);
	});

	it("returns false for empty message", () => {
		expect(isNetworkError(new Error(""))).toBe(false);
	});
});

describe("isAuthError", () => {
	it("returns true for 'api key' in message", () => {
		expect(isAuthError(new Error("invalid api key"))).toBe(true);
	});

	it("returns true for 'authentication'", () => {
		expect(isAuthError(new Error("authentication failed"))).toBe(true);
	});

	it("returns true for 'unauthorized'", () => {
		expect(isAuthError(new Error("Unauthorized access"))).toBe(true);
	});

	it("returns true for 'invalid credentials'", () => {
		expect(isAuthError(new Error("invalid credentials provided"))).toBe(true);
	});

	it("returns true for 'auth'", () => {
		expect(isAuthError(new Error("auth token expired"))).toBe(true);
	});

	it("returns true for '401'", () => {
		expect(isAuthError(new Error("received 401 status"))).toBe(true);
	});

	it("returns false for unrelated errors", () => {
		expect(isAuthError(new Error("network failure"))).toBe(false);
	});

	it("returns false for empty message", () => {
		expect(isAuthError(new Error(""))).toBe(false);
	});
});

describe("handleError", () => {
	let errored: string[];
	let originalConsoleError: typeof console.error;

	beforeEach(() => {
		setColorEnabled(false);
		originalConsoleError = console.error;
		errored = [];
		console.error = (...args: unknown[]) => {
			errored.push(args.map(String).join(" "));
		};
	});

	afterEach(() => {
		console.error = originalConsoleError;
		setColorEnabled(true);
	});

	it("prints error to stderr", () => {
		handleError(new Error("something broke"));
		expect(errored.length).toBe(1);
		expect(errored[0]).toContain("✗ Error:");
	});

	it("uses context.brief when provided", () => {
		handleError(new Error("internal"), { brief: "user-facing brief" });
		expect(errored[0]).toContain("user-facing brief");
	});

	it("uses context.detail when provided", () => {
		handleError(new Error("internal"), { detail: "extra detail" });
		expect(errored[0]).toContain("extra detail");
	});

	it("uses context.suggestion when provided", () => {
		handleError(new Error("internal"), { suggestion: "try this" });
		expect(errored[0]).toContain("Suggestion: try this");
	});

	it("auto-suggests network check for network errors", () => {
		handleError(new Error("network failure"));
		expect(errored[0]).toContain("internet connection");
	});

	it("auto-suggests auth login for auth errors", () => {
		handleError(new Error("401 unauthorized"));
		expect(errored[0]).toContain("chunk auth login");
	});

	it("auto-suggests generic message for unknown errors", () => {
		handleError(new Error("something unexpected"));
		expect(errored[0]).toContain("Check the error details");
	});

	it("handles non-Error objects by converting to Error", () => {
		handleError("a string error");
		expect(errored.length).toBe(1);
	});
});
