/**
 * Pure decision logic for `chunk run`.
 */

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
