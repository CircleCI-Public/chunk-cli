import packageJson from "../../package.json";
import type { CommandResult } from "../types";

export async function runVersion(): Promise<CommandResult> {
	console.log(`chunk ${packageJson.version}`);
	return { exitCode: 0 };
}
