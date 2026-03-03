/**
 * Unit Tests — ANSI color utilities
 */

import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import {
	bold,
	cyan,
	dim,
	gray,
	green,
	isColorEnabled,
	red,
	setColorEnabled,
	yellow,
} from "../ui/colors";

describe("setColorEnabled / isColorEnabled", () => {
	it("starts with color enabled (default)", () => {
		// restore after test
		const original = isColorEnabled();
		setColorEnabled(true);
		expect(isColorEnabled()).toBe(true);
		setColorEnabled(original);
	});

	it("disables color", () => {
		const original = isColorEnabled();
		setColorEnabled(false);
		expect(isColorEnabled()).toBe(false);
		setColorEnabled(original);
	});

	it("re-enables color", () => {
		const original = isColorEnabled();
		setColorEnabled(false);
		setColorEnabled(true);
		expect(isColorEnabled()).toBe(true);
		setColorEnabled(original);
	});
});

describe("color functions with colors disabled", () => {
	beforeEach(() => setColorEnabled(false));
	afterEach(() => setColorEnabled(true));

	it("red returns plain text", () => {
		expect(red("hello")).toBe("hello");
	});

	it("green returns plain text", () => {
		expect(green("hello")).toBe("hello");
	});

	it("yellow returns plain text", () => {
		expect(yellow("hello")).toBe("hello");
	});

	it("cyan returns plain text", () => {
		expect(cyan("hello")).toBe("hello");
	});

	it("gray returns plain text", () => {
		expect(gray("hello")).toBe("hello");
	});

	it("bold returns plain text", () => {
		expect(bold("hello")).toBe("hello");
	});

	it("dim returns plain text", () => {
		expect(dim("hello")).toBe("hello");
	});
});

describe("color functions with colors enabled", () => {
	beforeEach(() => setColorEnabled(true));
	afterEach(() => setColorEnabled(true));

	it("red wraps text with ANSI codes", () => {
		const result = red("hello");
		expect(result).toContain("hello");
		expect(result).toContain("\x1b[31m");
		expect(result).toContain("\x1b[0m");
	});

	it("green wraps text with ANSI codes", () => {
		const result = green("hello");
		expect(result).toContain("hello");
		expect(result).toContain("\x1b[32m");
		expect(result).toContain("\x1b[0m");
	});

	it("yellow wraps text with ANSI codes", () => {
		const result = yellow("hello");
		expect(result).toContain("hello");
		expect(result).toContain("\x1b[33m");
		expect(result).toContain("\x1b[0m");
	});

	it("cyan wraps text with ANSI codes", () => {
		const result = cyan("hello");
		expect(result).toContain("hello");
		expect(result).toContain("\x1b[36m");
		expect(result).toContain("\x1b[0m");
	});

	it("gray wraps text with ANSI codes", () => {
		const result = gray("hello");
		expect(result).toContain("hello");
		expect(result).toContain("\x1b[90m");
		expect(result).toContain("\x1b[0m");
	});

	it("bold wraps text with ANSI codes", () => {
		const result = bold("hello");
		expect(result).toContain("hello");
		expect(result).toContain("\x1b[1m");
		expect(result).toContain("\x1b[0m");
	});

	it("dim wraps text with ANSI codes", () => {
		const result = dim("hello");
		expect(result).toContain("hello");
		expect(result).toContain("\x1b[2m");
		expect(result).toContain("\x1b[0m");
	});

	it("handles empty string", () => {
		expect(red("")).toContain("\x1b[31m");
	});
});
