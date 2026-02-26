/**
 * Unit Tests - Terminal Output Formatting Primitives
 */

import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "bun:test";
import { setColorEnabled } from "../ui/colors";
import {
	formatStep,
	formatSuccess,
	formatWarning,
	label,
	printSuccess,
	printWarning,
} from "../ui/format";

describe("format functions", () => {
	beforeAll(() => setColorEnabled(false));
	afterAll(() => setColorEnabled(true));

	it("formatSuccess prepends checkmark", () => {
		expect(formatSuccess("done")).toBe("✓ done");
	});

	it("formatWarning prepends warning symbol", () => {
		expect(formatWarning("uh oh")).toBe("⚠ uh oh");
	});

	it("formatStep formats step header with dim counter and bold title", () => {
		expect(formatStep(1, 3, "Discovering Top Reviewers")).toBe(
			"Step 1/3  Discovering Top Reviewers",
		);
	});
});

describe("label", () => {
	beforeEach(() => setColorEnabled(false));
	afterEach(() => setColorEnabled(true));

	it("pads short text to the given width", () => {
		expect(label("Name:", 10)).toBe("Name:     ");
	});
});

describe("print functions", () => {
	let originalLog: typeof console.log;
	let originalError: typeof console.error;
	let logged: string[];
	let errored: string[];

	beforeEach(() => setColorEnabled(false));
	afterEach(() => setColorEnabled(true));

	beforeEach(() => {
		originalLog = console.log;
		originalError = console.error;
		logged = [];
		errored = [];
		console.log = (...args: unknown[]) => {
			logged.push(args.map(String).join(" "));
		};
		console.error = (...args: unknown[]) => {
			errored.push(args.map(String).join(" "));
		};
	});

	afterEach(() => {
		console.log = originalLog;
		console.error = originalError;
	});

	it("printSuccess writes to stdout with preceding newline", () => {
		printSuccess("done");
		expect(logged).toEqual(["\n✓ done"]);
		expect(errored).toEqual([]);
	});

	it("printWarning writes to stdout with preceding newline", () => {
		printWarning("uh oh");
		expect(logged).toEqual(["\n⚠ uh oh"]);
		expect(errored).toEqual([]);
	});

	it("printSuccess writes to stderr when specified", () => {
		printSuccess("done", "stderr");
		expect(errored).toEqual(["\n✓ done"]);
		expect(logged).toEqual([]);
	});

	it("printWarning writes to stderr when specified", () => {
		printWarning("uh oh", "stderr");
		expect(errored).toEqual(["\n⚠ uh oh"]);
		expect(logged).toEqual([]);
	});
});
