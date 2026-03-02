export type ExitCode = 0 | 1 | 2;

export interface CommandResult {
	exitCode: ExitCode;
	message?: string;
}
