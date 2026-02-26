import { performUpgrade } from "../core/upgrade";
import type { CommandResult, ParsedArgs } from "../types";

export function showUpgradeHelp(): void {
	console.log(`
Usage: chunk upgrade [options]

Update to the latest version

Options:
  -h, --help    Show this help message

Examples:
  chunk upgrade    Update to latest version
`);
}

export async function runUpgrade(parsed: ParsedArgs): Promise<CommandResult> {
	if (parsed.flags.help) {
		showUpgradeHelp();
		return { exitCode: 0 };
	}

	try {
		await performUpgrade();
	} catch (err: unknown) {
		const message = err instanceof Error ? err.message : String(err);
		console.error(`Failure running upgrade: ${message}`);
		return { exitCode: 1 };
	}

	return { exitCode: 0 };
}
