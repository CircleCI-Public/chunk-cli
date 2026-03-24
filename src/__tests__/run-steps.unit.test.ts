import { describe, expect, it } from "bun:test";
import { shouldPromptSave } from "../core/run.steps";

describe("core/run.steps", () => {
	describe("shouldPromptSave", () => {
		it("returns skip when no cmd provided", () => {
			expect(
				shouldPromptSave({
					isTTY: true,
					saveFlag: false,
					cmdProvided: false,
					existsInConfig: false,
				}),
			).toBe("skip");
		});

		it("returns save when --save flag is set", () => {
			expect(
				shouldPromptSave({ isTTY: true, saveFlag: true, cmdProvided: true, existsInConfig: false }),
			).toBe("save");
		});

		it("returns save when --save flag set even if exists in config", () => {
			expect(
				shouldPromptSave({ isTTY: true, saveFlag: true, cmdProvided: true, existsInConfig: true }),
			).toBe("save");
		});

		it("returns prompt when cmd provided, not in config, and TTY", () => {
			expect(
				shouldPromptSave({
					isTTY: true,
					saveFlag: false,
					cmdProvided: true,
					existsInConfig: false,
				}),
			).toBe("prompt");
		});

		it("returns skip when cmd provided, not in config, and not TTY", () => {
			expect(
				shouldPromptSave({
					isTTY: false,
					saveFlag: false,
					cmdProvided: true,
					existsInConfig: false,
				}),
			).toBe("skip");
		});

		it("returns skip when cmd provided and already in config (override, no save)", () => {
			expect(
				shouldPromptSave({ isTTY: true, saveFlag: false, cmdProvided: true, existsInConfig: true }),
			).toBe("skip");
		});
	});
});
