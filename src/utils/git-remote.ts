/**
 * Parse a GitHub remote URL into org and repo components.
 * Supports SSH, HTTPS, and ssh:// protocol URLs.
 */
export function parseGitRemoteUrl(url: string): { org: string; repo: string } | null {
	// Match patterns:
	// git@github.com:org/repo.git
	// https://github.com/org/repo.git
	// https://github.com/org/repo
	// ssh://git@github.com/org/repo.git
	const match = url.match(/github\.com[:/]([A-Za-z0-9_.-]+)\/([A-Za-z0-9_.-]+?)(?:\.git)?$/);
	if (!match?.[1] || !match[2]) return null;
	return { org: match[1], repo: match[2] };
}

/**
 * Get the URL for a git remote (defaults to "origin").
 * Returns null if the command fails (e.g., not in a git repo).
 */
export async function getRemoteUrl(remote = "origin"): Promise<string | null> {
	try {
		const proc = Bun.spawn(["git", "remote", "get-url", remote], {
			stdout: "pipe",
			stderr: "pipe",
		});
		// Read both streams concurrently to prevent pipe buffer deadlock
		const [stdout] = await Promise.all([
			new Response(proc.stdout).text(),
			new Response(proc.stderr).text(),
		]);
		const exitCode = await proc.exited;
		if (exitCode !== 0) return null;
		return stdout.trim() || null;
	} catch {
		return null;
	}
}

/**
 * Auto-detect the GitHub org and repo from the current git remote.
 * Throws descriptive errors if detection fails.
 */
export async function detectGitHubOrgAndRepo(): Promise<{ org: string; repo: string }> {
	const url = await getRemoteUrl();
	if (!url) {
		throw new Error(
			"Could not detect GitHub remote. Are you in a git repository? Use --org and --repos to specify manually.",
		);
	}

	const parsed = parseGitRemoteUrl(url);
	if (!parsed) {
		throw new Error(
			"Git remote URL does not appear to be a GitHub repository. Use --org and --repos to specify manually.",
		);
	}

	return parsed;
}
