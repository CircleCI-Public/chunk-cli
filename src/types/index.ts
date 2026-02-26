export type ExitCode = 0 | 1 | 2;

export interface CommandResult {
	exitCode: ExitCode;
	message?: string;
}

export interface ParsedArgs {
	command: string;
	subcommand?: string;
	args: string[];
	flags: Record<string, string | boolean>;
}
