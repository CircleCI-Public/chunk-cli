import packageJson from "../../package.json";
import { dim } from "../ui/colors";
import { formatSuccess, label, printSuccess } from "../ui/format";

export class UpgradeError extends Error {
	constructor(message: string) {
		super(message);
		this.name = "UpgradeError";
	}
}

function detectPlatform(): string {
	const arch = process.arch === "arm64" ? "arm64" : "x64";
	const os = process.platform === "darwin" ? "darwin" : "linux";
	return `${os}-${arch}`;
}

async function exec(cmd: string[]): Promise<{ stdout: string; stderr: string; exitCode: number }> {
	const proc = Bun.spawn(cmd, { stdout: "pipe", stderr: "pipe" });
	const exitCode = await proc.exited;
	const stdout = await new Response(proc.stdout).text();
	const stderr = await new Response(proc.stderr).text();
	return { stdout: stdout.trim(), stderr: stderr.trim(), exitCode };
}

function manualFallbackHint(platform: string): string {
	return (
		"Manual fallback:\n" +
		`  gh release download --repo circleci/code-review-cli -p 'chunk-${platform}' -D /tmp\n` +
		`  install /tmp/chunk-${platform} ~/.local/bin/chunk`
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

	const beforeVersion = packageJson.version;
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
		"circleci/code-review-cli",
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

	const tmpPath = `${binPath}.tmp`;

	try {
		// Download latest binary
		const downloadResult = await exec([
			"gh",
			"release",
			"download",
			"--repo",
			"circleci/code-review-cli",
			"--pattern",
			`chunk-${platform}`,
			"--output",
			tmpPath,
			"--clobber",
		]);

		if (downloadResult.exitCode !== 0) {
			throw new UpgradeError(
				`Download failed: ${downloadResult.stderr}\n\n${manualFallbackHint(platform)}`,
			);
		}

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
		// Clean up temp file on error
		await exec(["rm", "-f", tmpPath]);
		const message = error instanceof Error ? error.message : String(error);
		throw new UpgradeError(`Upgrade failed: ${message}`);
	}
}
