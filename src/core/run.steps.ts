/**
 * Pure decision logic for `chunk run`.
 */

import { bold, dim, gray } from "../ui/colors";

export function formatCommandList(
	commands: Array<{ name: string; run: string; description: string }>,
): string {
	if (commands.length === 0) return "";

	const maxName = Math.max(...commands.map((c) => c.name.length));
	const lines = commands.map((c) => {
		const padded = c.name.padEnd(maxName);
		const desc = c.description ? `  ${dim(c.description)}` : "";
		return `  ${bold(padded)}  ${gray(c.run)}${desc}`;
	});

	return lines.join("\n");
}

export function shouldPromptSave(ctx: {
	isTTY: boolean;
	saveFlag: boolean;
	cmdProvided: boolean;
	existsInConfig: boolean;
}): "save" | "prompt" | "skip" {
	if (!ctx.cmdProvided) return "skip";
	if (ctx.saveFlag) return "save";
	if (!ctx.existsInConfig && ctx.isTTY) return "prompt";
	return "skip";
}
