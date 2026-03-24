import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { getCircleCIToken } from "../utils/tokens";

const originalCircleToken = process.env.CIRCLE_TOKEN;
const originalCircleCIToken = process.env.CIRCLECI_TOKEN;

describe("getCircleCIToken", () => {
	beforeEach(() => {
		delete process.env.CIRCLE_TOKEN;
		delete process.env.CIRCLECI_TOKEN;
	});

	afterEach(() => {
		if (originalCircleToken !== undefined) {
			process.env.CIRCLE_TOKEN = originalCircleToken;
		} else {
			delete process.env.CIRCLE_TOKEN;
		}
		if (originalCircleCIToken !== undefined) {
			process.env.CIRCLECI_TOKEN = originalCircleCIToken;
		} else {
			delete process.env.CIRCLECI_TOKEN;
		}
	});

	it("returns CIRCLE_TOKEN when set", () => {
		process.env.CIRCLE_TOKEN = "circle-token";
		expect(getCircleCIToken()).toBe("circle-token");
	});

	it("returns CIRCLECI_TOKEN when only the fallback is set", () => {
		process.env.CIRCLECI_TOKEN = "circleci-token";
		expect(getCircleCIToken()).toBe("circleci-token");
	});

	it("prefers CIRCLE_TOKEN over CIRCLECI_TOKEN when both are set", () => {
		process.env.CIRCLE_TOKEN = "preferred";
		process.env.CIRCLECI_TOKEN = "fallback";
		expect(getCircleCIToken()).toBe("preferred");
	});

	it("returns undefined when neither token is set", () => {
		expect(getCircleCIToken()).toBeUndefined();
	});
});
