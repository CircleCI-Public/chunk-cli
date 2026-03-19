export type ExitCode = 0 | 1 | 2;

export interface CommandError {
	title: string;
	detail: string;
	suggestion?: string;
}

export interface CommandResult {
	exitCode: ExitCode;
	message?: string;
	error?: CommandError;
	data?: unknown;
}
