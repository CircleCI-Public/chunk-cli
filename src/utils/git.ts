/**
 * Get the current local branch name.
 * Returns null if not in a git repo or in detached HEAD state.
 */
export async function getCurrentBranch(cwd = process.cwd()): Promise<string | null> {
	const proc = Bun.spawn(["git", "rev-parse", "--abbrev-ref", "HEAD"], {
		cwd,
		stdout: "pipe",
		stderr: "pipe",
	});
	const [stdout] = await Promise.all([
		new Response(proc.stdout).text(),
		new Response(proc.stderr).text(),
	]);
	const exitCode = await proc.exited;
	if (exitCode !== 0) return null;
	const branch = stdout.trim();
	return branch === "HEAD" ? null : branch;
}

export async function resolveRemoteBase(
	cwd = process.cwd(),
): Promise<{ sha: string; type: "merge-base" | "origin-head" } | null> {
	// Use the merge-base between the upstream and origin/HEAD so the resulting
	// SHA is a commit the sandbox (cloned from the default branch) is guaranteed
	// to have, regardless of whether the user's branch has been pushed.
	const mergeBaseProc = Bun.spawn(["git", "merge-base", "@{upstream}", "origin/HEAD"], {
		cwd,
		stdout: "pipe",
		stderr: "pipe",
	});
	const [mergeBaseOut] = await Promise.all([
		new Response(mergeBaseProc.stdout).text(),
		new Response(mergeBaseProc.stderr).text(),
	]);
	await mergeBaseProc.exited;
	if (mergeBaseProc.exitCode === 0) {
		const sha = mergeBaseOut.trim();
		if (sha) return { sha, type: "merge-base" };
	}

	const originProc = Bun.spawn(["git", "rev-parse", "origin/HEAD"], {
		cwd,
		stdout: "pipe",
		stderr: "pipe",
	});
	const [originOut] = await Promise.all([
		new Response(originProc.stdout).text(),
		new Response(originProc.stderr).text(),
	]);
	await originProc.exited;
	if (originProc.exitCode === 0) {
		const sha = originOut.trim();
		if (sha) return { sha, type: "origin-head" };
	}

	return null;
}

export async function generatePatch(cwd: string, base: string): Promise<string> {
	const lsProc = Bun.spawn(["git", "ls-files", "--others", "--exclude-standard"], {
		cwd,
		stdout: "pipe",
		stderr: "inherit",
	});
	const lsOutput = await new Response(lsProc.stdout).text();
	await lsProc.exited;

	const untrackedFiles = lsOutput.trim().split("\n").filter(Boolean);

	if (untrackedFiles.length > 0) {
		const addProc = Bun.spawn(["git", "add", "-N", ...untrackedFiles], {
			cwd,
			stdout: "inherit",
			stderr: "inherit",
		});
		await addProc.exited;
	}

	const diffProc = Bun.spawn(["git", "diff", base, "--binary"], {
		cwd,
		stdout: "pipe",
		stderr: "inherit",
	});
	const patch = await new Response(diffProc.stdout).text();
	await diffProc.exited;

	if (untrackedFiles.length > 0) {
		const resetProc = Bun.spawn(["git", "reset", "HEAD", "--", ...untrackedFiles], {
			cwd,
			stdout: "inherit",
			stderr: "inherit",
		});
		await resetProc.exited;
	}

	return patch;
}
