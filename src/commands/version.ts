import packageJson from "../../package.json";
import { DEFAULT_MODEL } from "../config";
import type { CommandResult } from "../types";
import { bold, dim } from "../ui/colors";
import { printBanner } from "../ui/logo";

export async function runVersion(): Promise<CommandResult> {
	printBanner([
		`${bold("chunk")} ${dim(`v${packageJson.version}`)}`,
		dim(`Default model: ${DEFAULT_MODEL}`),
	]);
	return { exitCode: 0 };
}
