import type { ParsedArgs } from "../types";

export function parseArgs(argv: string[]): ParsedArgs {
	const args = argv.slice(2);
	const result: ParsedArgs = {
		command: "help",
		args: [],
		flags: {},
	};

	for (let i = 0; i < args.length; i++) {
		const arg = args[i];
		if (arg === undefined) continue;

		if (arg.startsWith("--")) {
			const key = arg.slice(2);
			const nextArg = args[i + 1];

			if (
				key === "help" ||
				key === "version" ||
				key === "print" ||
				key === "no-color" ||
				key === "verbose" ||
				key === "stats" ||
				key === "debug-log" ||
				key === "no-execution-log" ||
				key === "include-attribution"
			) {
				result.flags[key] = true;
			} else if (
				key === "max-turns" ||
				key === "commits" ||
				key === "log-destination" ||
				key === "allowed-tools" ||
				key === "disallowed-tools" ||
				key === "execution-log-file" ||
				key === "debug-log-file" ||
				key === "prompt-file"
			) {
				if (nextArg !== undefined && !nextArg.startsWith("-")) {
					result.flags[key] = nextArg;
					i++;
				}
			} else if (nextArg !== undefined && !nextArg.startsWith("-")) {
				result.flags[key] = nextArg;
				i++;
			} else {
				result.flags[key] = true;
			}
		} else if (arg.startsWith("-") && arg.length > 1) {
			const key = arg.slice(1);
			const nextArg = args[i + 1];

			const shortFlags: Record<string, string> = {
				h: "help",
				v: "version",
				n: "limit",
				r: "repo",
				s: "status",
			};

			const longKey = shortFlags[key] ?? key;

			if (longKey === "help" || longKey === "version") {
				result.flags[longKey] = true;
			} else if (nextArg !== undefined && !nextArg.startsWith("-")) {
				result.flags[longKey] = nextArg;
				i++;
			} else {
				result.flags[longKey] = true;
			}
		} else {
			const commands = [
				"help",
				"review",
				"run",
				"auth",
				"history",
				"config",
				"upgrade",
				"version",
				"build-prompt",
			];
			if (commands.includes(arg) && result.command === "help" && !result.subcommand) {
				result.command = arg;
			} else if (result.subcommand === undefined) {
				result.subcommand = arg;
			} else {
				result.args.push(arg);
			}
		}
	}

	return result;
}
