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

	// Collect stdout and stderr as text. `new Response(stream).text()` is
	// Bun's canonical way to read a piped stream and resolves once the
	// underlying pipe closes (process exits or is killed).
	const stdoutText = new Response(proc.stdout).text();
	const stderrText = new Response(proc.stderr).text();

	if (timeout > 0) {
		await Promise.race([
			proc.exited,
			new Promise<void>((resolve) => {
				timer = setTimeout(() => {
					timedOut = true;
					proc.kill("SIGTERM");
					// Force-kill after 5 s grace period
					setTimeout(() => proc.kill("SIGKILL"), 5_000);
					resolve();
				}, timeout * 1000);
			}),
		]);
	} else {
		await proc.exited;
	}

	if (timer) clearTimeout(timer);

	// Drain output: wait for stdout/stderr streams to close fully.
	// For normal exits this is near-instant. For timed-out processes we use a
	// longer window since SIGKILL may take up to 5 s to take effect.
	// If child processes hold pipes open past the window, we return what we have.
	const DRAIN_MS = timedOut ? 6_000 : 500;
	const [stdout, stderr] = await Promise.race([
		Promise.all([stdoutText, stderrText]),
		new Promise<[string, string]>((resolve) => setTimeout(() => resolve(["", ""]), DRAIN_MS)),
	]);

	const combined = (stdout + stderr).trim();
	const output = combined.length > maxOutput ? combined.slice(-maxOutput) : combined;

	return {
		exitCode: timedOut ? 124 : (proc.exitCode ?? 1),
		output,
		command,
	};
}
