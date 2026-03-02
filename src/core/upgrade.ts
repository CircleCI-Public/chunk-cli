import { dim } from "../ui/colors";
import { formatSuccess, label, printSuccess } from "../ui/format";

export class UpgradeError extends Error {
	constructor(message: string) {
		super(message);
		this.name = "UpgradeError";
	}
}

// Returns the platform suffix used in goreleaser archive names, e.g. "Darwin_arm64" or "Linux_x86_64"
function detectPlatform(): string {
	const arch = process.arch === "arm64" ? "arm64" : "x86_64";
	const os = process.platform === "darwin" ? "Darwin" : "Linux";
	return `${os}_${arch}`;
}

async function exec(cmd: string[]): Promise<{ stdout: string; stderr: string; exitCode: number }> {
	const proc = Bun.spawn(cmd, { stdout: "pipe", stderr: "pipe" });
	const exitCode = await proc.exited;
	const stdout = await new Response(proc.stdout).text();
	const stderr = await new Response(proc.stderr).text();
	return { stdout: stdout.trim(), stderr: stderr.trim(), exitCode };
}

function manualFallbackHint(platform: string): string {
	const archive = `chunk-cli_${platform}.tar.gz`;
	return (
		"Manual fallback:\n" +
		`  gh release download --repo CircleCI-Public/chunk-cli -p '${archive}' -D /tmp\n` +
		`  tar -xzf /tmp/${archive} -C /tmp chunk\n` +
		`  install /tmp/chunk ~/.local/bin/chunk`
	);
}

export async function performUpgrade(): Promise<boolean> {
	const platform = detectPlatform();
	const binPath = `${process.env.HOME}/.local/bin/chunk`;

	// Check gh CLI is available
	try {
		const ghCheck = await exec(["gh", "--version"]);
		if (ghCheck.exitCode !== 0) {
			throw new UpgradeError("Error running gh CLI.");
		}
	} catch (error: unknown) {
		const message = error instanceof Error ? error.message : String(error);
		throw new UpgradeError(
			"Error: " +
				message +
				"\n\n" +
				"You probably need to install the gh CLI from https://cli.github.com/ then run:\n" +
				"  gh auth login",
		);
	}

	// Check gh CLI is authenticated
	const authCheck = await exec(["gh", "auth", "status"]);
	if (authCheck.exitCode !== 0) {
		throw new UpgradeError("Error: gh CLI not authenticated.\n\n" + "Run: gh auth login");
	}

	const beforeVersion = VERSION;
	const W = 9; // align to "Platform:"
	console.log(`  ${label("Version:", W, dim)} ${beforeVersion}`);
	console.log(`  ${label("Platform:", W, dim)} ${platform}`);

	// Get the tag for just the latest release currently on Github.
	const getLatestTagResult = await exec([
		"gh",
		"release",
		"list",
		"--limit",
		"1",
		"--exclude-pre-releases",
		"--exclude-drafts",
		"--repo",
		"CircleCI-Public/chunk-cli",
		"--json",
		"tagName",
		"--jq",
		".[].tagName",
	]);

	if (getLatestTagResult.exitCode !== 0) {
		throw new UpgradeError("Error retrieving latest release. Aborting.");
	}

	const latestVersion = getLatestTagResult.stdout;

	if (latestVersion === beforeVersion) {
		console.log(`  ${formatSuccess("Up to date")}`);
		return false;
	}

	console.log(dim(`Downloading release with tag ${latestVersion}...`));

	const archive = `chunk-cli_${platform}.tar.gz`;
	const archivePath = `/tmp/${archive}`;
	const checksumsPath = "/tmp/chunk-cli-checksums.txt";
	const tmpPath = `${binPath}.tmp`;

	try {
		// Download release archive and checksums in parallel
		const [downloadResult, checksumsResult] = await Promise.all([
			exec([
				"gh",
				"release",
				"download",
				"--repo",
				"CircleCI-Public/chunk-cli",
				"--pattern",
				archive,
				"--output",
				archivePath,
				"--clobber",
			]),
			exec([
				"gh",
				"release",
				"download",
				"--repo",
				"CircleCI-Public/chunk-cli",
				"--pattern",
				"checksums.txt",
				"--output",
				checksumsPath,
				"--clobber",
			]),
		]);

		if (downloadResult.exitCode !== 0) {
			throw new UpgradeError(
				`Download failed: ${downloadResult.stderr}\n\n${manualFallbackHint(platform)}`,
			);
		}
		if (checksumsResult.exitCode !== 0) {
			throw new UpgradeError(`Failed to download checksums.txt: ${checksumsResult.stderr}`);
		}

		// Verify checksum
		const checksums = await Bun.file(checksumsPath).text();
		await exec(["rm", "-f", checksumsPath]);
		const line = checksums.split("\n").find((l) => l.includes(archive));
		const expectedChecksum = line?.split(/\s+/)[0];
		if (!expectedChecksum) {
			throw new UpgradeError(`Checksum for ${archive} not found in checksums.txt`);
		}
		const shaCmd = process.platform === "darwin" ? ["shasum", "-a", "256"] : ["sha256sum"];
		const shaResult = await exec([...shaCmd, archivePath]);
		const actualChecksum = shaResult.stdout.split(/\s+/)[0];
		if (actualChecksum !== expectedChecksum) {
			throw new UpgradeError(
				`Checksum mismatch for ${archive}\n  expected: ${expectedChecksum}\n  actual:   ${actualChecksum}`,
			);
		}

		// Extract the binary from the archive
		const extractResult = await exec(["tar", "-xzf", archivePath, "-C", "/tmp", "chunk"]);
		await exec(["rm", "-f", archivePath]);
		if (extractResult.exitCode !== 0) {
			throw new UpgradeError(
				`Extraction failed: ${extractResult.stderr}\n\n${manualFallbackHint(platform)}`,
			);
		}
		await exec(["mv", "/tmp/chunk", tmpPath]);

		// Make executable
		const chmodResult = await exec(["chmod", "+x", tmpPath]);
		if (chmodResult.exitCode !== 0) {
			throw new UpgradeError("Failed to set executable permission");
		}

		// Verify the downloaded binary works
		const verifyResult = await exec([tmpPath, "--version"]);
		if (verifyResult.exitCode !== 0) {
			await exec(["rm", "-f", tmpPath]);
			throw new UpgradeError(
				`Downloaded binary failed verification.\n\n${manualFallbackHint(platform)}`,
			);
		}

		const afterVersion = verifyResult.stdout;

		// Atomic replace (mv preserves the executable permission set above)
		const mvResult = await exec(["mv", tmpPath, binPath]);
		if (mvResult.exitCode !== 0) {
			throw new UpgradeError(`Failed to install binary: ${mvResult.stderr}`);
		}

		printSuccess(`Updated: ${beforeVersion} → ${afterVersion}`);

		return true;
	} catch (error) {
		// Clean up temp files on error
		await exec(["rm", "-f", tmpPath, archivePath, checksumsPath]);
		const message = error instanceof Error ? error.message : String(error);
		throw new UpgradeError(`Upgrade failed: ${message}`);
	}
}
