/**
 * Process runner with timeout support.
 *
 * Uses `Bun.spawn` to run child processes, capturing stdout+stderr and
 * enforcing a configurable timeout.
 */

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
	const proc = Bun.spawn(["sh", "-c", command], {
		cwd,
		env: { ...baseEnv, ...extraEnv },
		stdout: "pipe",
		stderr: "pipe",
	});

	let timedOut = false;
	let timer: ReturnType<typeof setTimeout> | undefined;

	const timeoutPromise =
		timeout > 0
			? new Promise<void>((resolve) => {
					timer = setTimeout(() => {
						timedOut = true;
						proc.kill("SIGTERM");
						// Force-kill after 5 s grace period
						setTimeout(() => proc.kill("SIGKILL"), 5_000);
						resolve();
					}, timeout * 1000);
				})
			: undefined;

	// Collect output streams
	const [stdout, stderr] = await Promise.all([
		new Response(proc.stdout).text(),
		new Response(proc.stderr).text(),
	]);

	if (timeoutPromise) {
		// Race the process exit against the timeout
		await Promise.race([proc.exited, timeoutPromise]);
	} else {
		await proc.exited;
	}

	if (timer) clearTimeout(timer);

	const combined = (stdout + stderr).trim();
	const output = combined.length > maxOutput ? combined.slice(-maxOutput) : combined;

	return {
		exitCode: timedOut ? 124 : (proc.exitCode ?? 1),
		output,
		command,
	};
}
