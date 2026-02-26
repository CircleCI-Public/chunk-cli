#!/usr/bin/env bun
import { existsSync, statSync } from "node:fs";
import { join } from "node:path";
import packageJson from "../package.json";

const PLATFORMS = [
	"darwin-arm64",
	"darwin-x64",
	"linux-arm64",
	"linux-x64",
] as const;

const MIN_BINARY_SIZE_MB = 1;

interface ReleaseOptions {
	dryRun: boolean;
	draft: boolean;
}

function parseFlags(): ReleaseOptions {
	const args = process.argv.slice(2);
	return {
		dryRun: args.includes("--dry-run"),
		draft: args.includes("--draft"),
	};
}

async function exec(
	cmd: string[],
	opts?: { cwd?: string },
): Promise<{ stdout: string; stderr: string; exitCode: number }> {
	const proc = Bun.spawn(cmd, {
		cwd: opts?.cwd,
		stdout: "pipe",
		stderr: "pipe",
	});
	const exitCode = await proc.exited;
	const stdout = await new Response(proc.stdout).text();
	const stderr = await new Response(proc.stderr).text();
	return { stdout: stdout.trim(), stderr: stderr.trim(), exitCode };
}

async function checkGhCli(): Promise<void> {
	const { exitCode } = await exec(["gh", "--version"]);
	if (exitCode !== 0) {
		throw new Error(
			"gh CLI not found. Install it: https://cli.github.com/",
		);
	}

	const auth = await exec(["gh", "auth", "status"]);
	if (auth.exitCode !== 0) {
		throw new Error("gh CLI not authenticated. Run: gh auth login");
	}
}

async function checkCleanWorkingDir(): Promise<void> {
	const { stdout } = await exec(["git", "status", "--porcelain"]);
	if (stdout.length > 0) {
		throw new Error(
			"Working directory has uncommitted changes. Commit or stash them first.",
		);
	}
}

async function checkOnMainBranch(): Promise<void> {
	const { stdout } = await exec(["git", "rev-parse", "--abbrev-ref", "HEAD"]);
	if (stdout !== "main") {
		throw new Error(
			`Must be on 'main' branch to release. Currently on '${stdout}'.`,
		);
	}
}

async function checkTagDoesNotExist(tag: string): Promise<void> {
	const { exitCode } = await exec(["git", "rev-parse", tag]);
	if (exitCode === 0) {
		throw new Error(
			`Tag '${tag}' already exists. Bump the version in package.json first.`,
		);
	}
}

function validateBinaries(distDir: string): Map<string, number> {
	const sizes = new Map<string, number>();

	for (const platform of PLATFORMS) {
		const binaryPath = join(distDir, `chunk-${platform}`);
		if (!existsSync(binaryPath)) {
			throw new Error(
				`Binary not found: ${binaryPath}\nRun 'bun run build' first.`,
			);
		}

		const stats = statSync(binaryPath);
		const sizeMB = stats.size / (1024 * 1024);

		if (sizeMB < MIN_BINARY_SIZE_MB) {
			throw new Error(
				`Binary too small: chunk-${platform} is ${sizeMB.toFixed(1)} MB (minimum: ${MIN_BINARY_SIZE_MB} MB)`,
			);
		}

		sizes.set(platform, sizeMB);
	}

	return sizes;
}

async function generateChecksums(distDir: string): Promise<string> {
	const lines: string[] = [];

	for (const platform of PLATFORMS) {
		const buffer = await Bun.file(join(distDir, `chunk-${platform}`)).arrayBuffer();
		const digest = new Bun.CryptoHasher("sha256").update(buffer).digest("hex");
		lines.push(`${digest}  chunk-${platform}`);
	}

	const checksumPath = join(distDir, "checksums.txt");
	await Bun.write(checksumPath, `${lines.join("\n")}\n`);
	return checksumPath;
}

function buildReleaseNotes(
	version: string,
	sizes: Map<string, number>,
): string {
	const checksumRows = PLATFORMS.map(
		(p) => `| chunk-${p} | ${sizes.get(p)?.toFixed(1)} MB |`,
	).join("\n");

	return `## chunk ${version}

### Installation

Download the binary for your platform and place it in your PATH:

\`\`\`bash
# macOS (Apple Silicon)
gh release download ${version} -p 'chunk-darwin-arm64' -D /tmp && install /tmp/chunk-darwin-arm64 ~/.local/bin/chunk

# macOS (Intel)
gh release download ${version} -p 'chunk-darwin-x64' -D /tmp && install /tmp/chunk-darwin-x64 ~/.local/bin/chunk

# Linux (arm64)
gh release download ${version} -p 'chunk-linux-arm64' -D /tmp && install /tmp/chunk-linux-arm64 ~/.local/bin/chunk

# Linux (x86_64)
gh release download ${version} -p 'chunk-linux-x64' -D /tmp && install /tmp/chunk-linux-x64 ~/.local/bin/chunk
\`\`\`

Or upgrade an existing installation:
\`\`\`bash
chunk upgrade
\`\`\`

### Binaries

| File | Size |
|------|------|
${checksumRows}

See \`checksums.txt\` for SHA-256 hashes.
`;
}

async function release(): Promise<void> {
	const opts = parseFlags();
	const rootDir = join(import.meta.dir, "..");
	const distDir = join(rootDir, "dist");
	const version = packageJson.version;
	const tag = `${version}`;

	const suffix = [
		opts.dryRun && "(dry run)",
		opts.draft && "(draft)",
	].filter(Boolean).join(" ");
	console.log(`🚀 Releasing chunk ${tag}${suffix ? ` ${suffix}` : ""}\n`);

	// Preflight checks
	console.log("Running preflight checks...");

	console.log("  Checking gh CLI...");
	await checkGhCli();
	console.log("  ✓ gh CLI authenticated");

	console.log("  Checking working directory...");
	await checkCleanWorkingDir();
	console.log("  ✓ Working directory clean");

	console.log("  Checking branch...");
	await checkOnMainBranch();
	console.log("  ✓ On main branch");

	console.log("  Checking tag...");
	await checkTagDoesNotExist(tag);
	console.log(`  ✓ Tag ${tag} is available`);

	console.log("");

	// Validate binaries
	console.log("Validating binaries...");
	const sizes = validateBinaries(distDir);
	for (const [platform, sizeMB] of sizes) {
		console.log(`  ✓ chunk-${platform}: ${sizeMB.toFixed(1)} MB`);
	}
	console.log("");

	// Generate checksums
	console.log("Generating checksums...");
	const checksumPath = await generateChecksums(distDir);
	console.log(`  ✓ Written to ${checksumPath}`);
	console.log("");

	// Build release notes
	const notes = buildReleaseNotes(version, sizes);

	if (opts.dryRun) {
		console.log("─".repeat(50));
		console.log("DRY RUN — would perform the following:\n");
		console.log(`  1. Create annotated tag: ${tag}`);
		console.log(`  2. Push tag to origin`);
		console.log(`  3. Create GitHub release${opts.draft ? " (draft)" : ""}`);
		console.log(`     Assets:`);
		for (const platform of PLATFORMS) {
			console.log(`       - chunk-${platform}`);
		}
		console.log(`       - checksums.txt`);
		console.log(`\n  Release notes preview:\n`);
		console.log(notes);
		console.log("─".repeat(50));
		console.log("\n✅ Dry run complete — everything looks good!");
		return;
	}

	// Create and push tag
	console.log(`Creating tag ${tag}...`);
	const tagResult = await exec([
		"git",
		"tag",
		"-a",
		tag,
		"-m",
		`Release ${tag}`,
	]);
	if (tagResult.exitCode !== 0) {
		throw new Error(`Failed to create tag: ${tagResult.stderr}`);
	}
	console.log(`  ✓ Tag ${tag} created`);

	console.log("Pushing tag...");
	const pushResult = await exec(["git", "push", "origin", tag]);
	if (pushResult.exitCode !== 0) {
		throw new Error(`Failed to push tag: ${pushResult.stderr}`);
	}
	console.log("  ✓ Tag pushed to origin");
	console.log("");

	// Create GitHub release
	console.log("Creating GitHub release...");
	const assets = [
		...PLATFORMS.map((p) => join(distDir, `chunk-${p}`)),
		checksumPath,
	];

	const ghArgs = [
		"gh",
		"release",
		"create",
		tag,
		...assets,
		"--title",
		`chunk ${tag}`,
		"--notes",
		notes,
		...(opts.draft ? ["--draft"] : []),
	];

	const releaseResult = await exec(ghArgs, { cwd: rootDir });
	if (releaseResult.exitCode !== 0) {
		throw new Error(
			`Failed to create release: ${releaseResult.stderr}`,
		);
	}

	console.log(`  ✓ Release created: ${releaseResult.stdout}`);
	console.log("");
	console.log(`✅ Released chunk ${tag}!`);
}

release().catch((err) => {
	console.error(`\n❌ Release failed: ${err.message}`);
	process.exit(1);
});
