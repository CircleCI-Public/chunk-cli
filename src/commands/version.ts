import { DEFAULT_MODEL } from "../config";
import type { CommandResult } from "../types";
import { bold, dim } from "../ui/colors";
import { printBanner } from "../ui/logo";
import { VERSION } from "../version";

export async function runVersion(): Promise<CommandResult> {
	printBanner([`${bold("chunk")} ${dim(`v${VERSION}`)}`, dim(`Default model: ${DEFAULT_MODEL}`)]);
	return { exitCode: 0 };
}
