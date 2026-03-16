/**
 * Process runner with timeout support.
 *
 * Uses Node.js `child_process.spawn` to run child processes, capturing
 * stdout+stderr and enforcing a configurable timeout.
 */
import { spawn } from "child_process";

/** Result of a child-process execution. */
export type RunResult = {
	exitCode: number;
	output: string;
	command: string;
};

/** Options for `runCommand`. */
export type RunOptions = {
	/** Shell command string to execute. */
	command: string;
	/** Working directory. Defaults to `process.cwd()`. */
	cwd?: string;
	/** Timeout in seconds. 0 means no timeout. Defaults to 300. */
	timeout?: number;
	/** Extra environment variables merged with `process.env`. */
	env?: Record<string, string>;
	/**
	 * When `true` (default), `env` is merged on top of `process.env`.
	 * When `false`, `env` is used as the complete environment — `process.env`
	 * is NOT inherited.  Use this to run commands in a clean shell environment.
	 */
	extendEnv?: boolean;
};

/**
 * Run a shell command and capture its output.
 *
 * Returns the combined stdout+stderr, exit code, and whether it timed out.
 * Output is truncated to the last 50 KB to keep result files manageable.
 */
export async function runCommand(opts: RunOptions): Promise<RunResult> {
	const { command, cwd = process.cwd(), timeout = 300, env: extraEnv, extendEnv = true } = opts;
	const maxOutput = 50 * 1024; // 50 KB

	const baseEnv = extendEnv ? process.env : {};
	const env = { ...baseEnv, ...extraEnv };

	return new Promise<RunResult>((resolve) => {
		const proc = spawn("sh", ["-c", command], { cwd, env });

		let timedOut = false;
		let timeoutTimer: ReturnType<typeof setTimeout> | undefined;
		let killTimer: ReturnType<typeof setTimeout> | undefined;

		const stdoutChunks: Buffer[] = [];
		const stderrChunks: Buffer[] = [];

		// EventEmitter-based streams are reliable across Bun versions on all
		// platforms. Data is buffered as it arrives; `close` fires only after
		// the process exits AND both streams are fully drained.
		proc.stdout?.on("data", (chunk: Buffer) => stdoutChunks.push(chunk));
		proc.stderr?.on("data", (chunk: Buffer) => stderrChunks.push(chunk));

		if (timeout > 0) {
			timeoutTimer = setTimeout(() => {
				timedOut = true;
				proc.kill("SIGTERM");
				// Force-kill after 5 s grace period
				killTimer = setTimeout(() => proc.kill("SIGKILL"), 5_000);
			}, timeout * 1000);
		}

		proc.on("close", (code) => {
			if (timeoutTimer) clearTimeout(timeoutTimer);
			if (killTimer) clearTimeout(killTimer);

			const combined = (
				Buffer.concat(stdoutChunks).toString("utf-8") +
				Buffer.concat(stderrChunks).toString("utf-8")
			).trim();
			const output = combined.length > maxOutput ? combined.slice(-maxOutput) : combined;

			resolve({
				exitCode: timedOut ? 124 : (code ?? 1),
				output,
				command,
			});
		});
	});
}
