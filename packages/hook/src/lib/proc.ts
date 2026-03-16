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
/**
 * Collect a ReadableStream into a string chunk-by-chunk, storing each decoded
 * chunk in `buffer` as it arrives. Returns a promise that resolves when the
 * stream closes. Callers can snapshot `buffer.join("")` at any time to get
 * whatever has been collected so far, even before the stream closes.
 */
async function collectStream(stream: ReadableStream<Uint8Array>, buffer: string[]): Promise<void> {
	const decoder = new TextDecoder();
	const reader = stream.getReader();
	try {
		while (true) {
			const { done, value } = await reader.read();
			// Push value before checking done: on Linux, Bun may return the final
			// chunk with done=true in the same read, and breaking first would
			// discard it (reproducible with short outputs like `echo hello`).
			if (value) {
				buffer.push(decoder.decode(value, { stream: !done }));
			}
			if (done) break;
		}
		// Flush any remaining bytes in the decoder
		const tail = decoder.decode();
		if (tail) buffer.push(tail);
	} finally {
		reader.releaseLock();
	}
}

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

	// Accumulate chunks into buffers as they arrive. This lets us snapshot
	// partial output if the drain window expires before the pipes close.
	const stdoutBuf: string[] = [];
	const stderrBuf: string[] = [];
	const stdoutDone = collectStream(proc.stdout, stdoutBuf);
	const stderrDone = collectStream(proc.stderr, stderrBuf);

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

	// Drain output: child processes spawned by sh may hold the pipes open after
	// sh exits. Wait up to 500 ms for the readers to finish; if they don't,
	// snapshot whatever has been collected so far rather than returning nothing.
	const DRAIN_MS = 500;
	await Promise.race([
		Promise.all([stdoutDone, stderrDone]),
		new Promise<void>((resolve) => setTimeout(resolve, DRAIN_MS)),
	]);

	const combined = (stdoutBuf.join("") + stderrBuf.join("")).trim();
	const output = combined.length > maxOutput ? combined.slice(-maxOutput) : combined;

	return {
		exitCode: timedOut ? 124 : (proc.exitCode ?? 1),
		output,
		command,
	};
}
