import type { CommandResult } from "../types";

export async function runPrep(): Promise<CommandResult> {
	console.log("preparing...");
	return { exitCode: 0 };
}
