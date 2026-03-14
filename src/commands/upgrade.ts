import type { Command } from "@commander-js/extra-typings";
import { performUpgrade } from "../core/upgrade";
import type { CommandResult } from "../types";

export function registerUpgradeCommand(program: Command): void {
	program
		.command("upgrade")
		.description("Update to the latest version")
		.action(async () => process.exit((await runUpgrade()).exitCode));
}

async function runUpgrade(): Promise<CommandResult> {
	try {
		await performUpgrade();
	} catch (err: unknown) {
		const message = err instanceof Error ? err.message : String(err);
		console.error(`Failure running upgrade: ${message}`);
		return { exitCode: 1 };
	}

	return { exitCode: 0 };
}
