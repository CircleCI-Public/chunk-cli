import { describe, expect, it } from "bun:test";
import { rewriteColonSyntax } from "../utils/argv";

describe("rewriteColonSyntax", () => {
	it("rewrites validate:<name> to validate <name>", () => {
		expect(rewriteColonSyntax(["node", "chunk", "validate:test"])).toEqual([
			"node",
			"chunk",
			"validate",
			"test",
		]);
	});

	it("leaves non-colon args unchanged", () => {
		const argv = ["node", "chunk", "validate", "test"];
		expect(rewriteColonSyntax(argv)).toEqual(argv);
	});

	it("does not rewrite validate: with no name (empty suffix)", () => {
		const argv = ["node", "chunk", "validate:"];
		expect(rewriteColonSyntax(argv)).toEqual(argv);
	});

	it("preserves extra colons in the name (validate:foo:bar → validate foo:bar)", () => {
		expect(rewriteColonSyntax(["node", "chunk", "validate:foo:bar"])).toEqual([
			"node",
			"chunk",
			"validate",
			"foo:bar",
		]);
	});

	it("rewrites only the first matching arg", () => {
		expect(rewriteColonSyntax(["node", "chunk", "validate:lint", "validate:test"])).toEqual([
			"node",
			"chunk",
			"validate",
			"lint",
			"validate:test",
		]);
	});

	it("preserves flags that appear after the colon command", () => {
		expect(rewriteColonSyntax(["node", "chunk", "validate:test", "--force"])).toEqual([
			"node",
			"chunk",
			"validate",
			"test",
			"--force",
		]);
	});

	it("does not rewrite flags before the colon command", () => {
		const argv = ["node", "chunk", "--debug", "validate:test"];
		expect(rewriteColonSyntax(argv)).toEqual(["node", "chunk", "--debug", "validate", "test"]);
	});

	it("returns unchanged argv when no args present", () => {
		expect(rewriteColonSyntax(["node", "chunk"])).toEqual(["node", "chunk"]);
	});
});
