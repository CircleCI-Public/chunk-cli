import { execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import Anthropic from "@anthropic-ai/sdk";
import { DEFAULT_MODEL } from "../../config";
import type { CommandResult } from "../../types";
import { bold } from "../../ui/colors";
import { promptInput } from "../../ui/prompt";
import { printError } from "../../utils/errors";

interface RequiredCredential {
	buildArg: string;
	description: string;
	sensitive: boolean;
}

function isGitRepo(cwd: string): boolean {
	try {
		execFileSync("git", ["rev-parse", "--git-dir"], { cwd, stdio: "ignore" });
		return true;
	} catch {
		return false;
	}
}

function gatherExistingDockerfiles(cwd: string): string {
	const parts: string[] = [];

	// Directories likely to contain Dockerfiles
	const searchDirs = [".", "docker", ".docker", "build", "ci", ".circleci", "infra", "deploy"];

	for (const dir of searchDirs) {
		const dirPath = join(cwd, dir);
		if (!existsSync(dirPath)) continue;

		let entries: string[];
		try {
			entries = readdirSync(dirPath);
		} catch {
			continue;
		}

		for (const entry of entries) {
			// Match Dockerfile, Dockerfile.*, dockerfile, dockerfile.*
			// but skip our own generated files
			if (!/^[Dd]ockerfile(\.[^.]+)?$/.test(entry) || entry.startsWith("Dockerfile.chunk")) {
				continue;
			}

			const rel = dir === "." ? entry : `${dir}/${entry}`;
			const full = join(cwd, rel);
			try {
				const content = readFileSync(full, "utf-8").slice(0, 4000);
				parts.push(`\n--- ${rel} ---\n${content}`);
			} catch {
				// ignore
			}
		}
	}

	// Also pick up compose files as they reveal service/build context
	const composeNames = ["docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"];
	for (const name of composeNames) {
		const full = join(cwd, name);
		if (existsSync(full)) {
			try {
				const content = readFileSync(full, "utf-8").slice(0, 4000);
				parts.push(`\n--- ${name} ---\n${content}`);
			} catch {
				// ignore
			}
		}
	}

	return parts.join("\n");
}

interface PackageManager {
	name: string;
	installCommand: string;
	lockfile: string;
}

function detectPackageManager(cwd: string): PackageManager | null {
	const managers: { lockfile: string; name: string; installCommand: string }[] = [
		{ lockfile: "pnpm-lock.yaml", name: "pnpm", installCommand: "pnpm install" },
		{ lockfile: "yarn.lock", name: "yarn", installCommand: "yarn install --frozen-lockfile" },
		{ lockfile: "bun.lock", name: "bun", installCommand: "bun install --frozen-lockfile" },
		{ lockfile: "bun.lockb", name: "bun", installCommand: "bun install --frozen-lockfile" },
		{ lockfile: "package-lock.json", name: "npm", installCommand: "npm ci" },
	];

	for (const m of managers) {
		if (existsSync(join(cwd, m.lockfile))) {
			return { name: m.name, installCommand: m.installCommand, lockfile: m.lockfile };
		}
	}
	return null;
}

function gatherRepoContext(cwd: string): string {
	const parts: string[] = [];

	// Root file listing
	try {
		const entries = readdirSync(cwd);
		parts.push(`Root files:\n${entries.join("\n")}`);
	} catch {
		// ignore
	}

	// Key config files that signal how tests are run and what dependencies are required
	const candidates = [
		"package.json",
		"Makefile",
		"go.mod",
		"pom.xml",
		"build.gradle",
		"build.gradle.kts",
		"pyproject.toml",
		"setup.py",
		"pytest.ini",
		"Cargo.toml",
		".chunk/hook/config.yml",
		// Registry and auth config — may reveal private dependencies
		// Registry and auth config — may reveal private dependencies
		".npmrc",
		".yarnrc",
		".yarnrc.yml",
		"pip.conf",
		".pip/pip.conf",
		".cargo/config.toml",
		"settings.xml",
		"gradle.properties",
		// Python dep files — may reference private indexes
		"requirements.txt",
		"requirements-dev.txt",
		"requirements-test.txt",
		"Pipfile",
		// Ruby
		"Gemfile",
		// Go
		"go.sum",
		// Clojure
		"project.clj",
		"deps.edn",
		"build.clj",
		"profiles.clj",
	];

	for (const rel of candidates) {
		const full = join(cwd, rel);
		if (existsSync(full)) {
			try {
				const content = readFileSync(full, "utf-8").slice(0, 4000);
				parts.push(`\n--- ${rel} ---\n${content}`);
			} catch {
				// ignore
			}
		}
	}

	return parts.join("\n");
}

function buildBaseImagePrompt(context: string, testCommand: string): string {
	return (
		`You are selecting a Docker base image for a software project.\n\n` +
		`Test command: ${testCommand}\n\n` +
		`Repository context:\n${context}\n\n` +
		`Output ONLY a JSON object with a single field "repository": the Docker Hub repository name ` +
		`for the most appropriate base image (e.g. "clojure", "node", "python", "golang", "rust"). ` +
		`For official images use just the name. For third-party images use "namespace/image". ` +
		`No tag — just the repository name. No explanation, no markdown.`
	);
}

function buildCredentialsPrompt(context: string, existingDockerfiles: string): string {
	return (
		`You are analyzing a software repository to identify private or non-public dependencies that require authentication credentials at build or install time.\n\n` +
		`Repository context:\n${context}\n\n` +
		(existingDockerfiles ? `Existing Dockerfiles:\n${existingDockerfiles}\n\n` : "") +
		`Look carefully for:\n` +
		`- Private npm registries (S3-backed, Artifactory, GitHub Packages, Verdaccio, etc.)\n` +
		`- Private PyPI indexes or --extra-index-url references\n` +
		`- Private Maven or Gradle repositories requiring credentials\n` +
		`- Private Cargo registries\n` +
		`- Private Go module proxies or GONOSUMCHECK patterns\n` +
		`- Any other indication that a dependency is fetched from a non-public source\n\n` +
		`For each required credential output a JSON array. Each element must have:\n` +
		`  "buildArg": the Docker ARG name (e.g. "AWS_ACCESS_KEY_ID", "NPM_TOKEN")\n` +
		`  "description": what it is and why it is needed\n` +
		`  "sensitive": true if it is a secret/token/password, false otherwise\n\n` +
		`Output ONLY the JSON array. If nothing private is detected, output [].`
	);
}

function buildTestCommandPrompt(context: string, packageManager: PackageManager | null): string {
	return (
		`You are analyzing a software repository to determine how tests are run.\n\n` +
		(packageManager
			? `Detected package manager: ${packageManager.name} (lockfile: ${packageManager.lockfile}). Use ${packageManager.name} to run tests (e.g. \`${packageManager.name} test\`).\n\n`
			: "") +
		`${context}\n\n` +
		`Based on the above, output ONLY the shell command used to run the test suite — ` +
		`nothing else. No explanation, no markdown. Just the command string.`
	);
}

function buildDockerfilePrompt(
	testCommand: string,
	context: string,
	existingDockerfiles: string,
	baseImageRepo: string | null,
	availableTags: string[],
	collectedCredentials: Record<string, string>,
	packageManager: PackageManager | null,
): string {
	return (
		`You are generating a Dockerfile to run tests for a software project in a CI environment.\n\n` +
		`Test command: ${testCommand}\n\n` +
		(packageManager
			? `Package manager: ${packageManager.name} (lockfile: ${packageManager.lockfile})\n` +
				`Install command: ${packageManager.installCommand}\n\n`
			: "") +
		`Repository context:\n${context}\n\n` +
		(existingDockerfiles
			? `Existing Dockerfiles in this repo (use as reference for base images, build steps, and patterns):\n${existingDockerfiles}\n\n`
			: "") +
		`Requirements:\n` +
		(availableTags.length > 0
			? `- Use ${baseImageRepo} as the base image. The following tags are currently available on Docker Hub — ` +
				`choose the most recent stable one by reasoning about the version numbers in the tag names:\n` +
				availableTags.map((t) => `  - ${t}`).join("\n") +
				`\n  Avoid tags marked alpha, beta, rc, snapshot, edge, or that reference very old major versions.\n`
			: `- Use an appropriate official base image from Docker Hub for the detected language and tooling.\n` +
				`  Pin a specific version tag — do not use "latest" — but aim for the most current stable release available.\n`) +
		`- Install any additional system-level dependencies needed to run the test command.\n` +
		(packageManager
			? `- Use \`${packageManager.installCommand}\` to install dependencies (not npm ci or npm install unless the project uses npm).\n` +
				(packageManager.name === "pnpm"
					? `- Install pnpm first: \`RUN corepack enable && corepack prepare pnpm --activate\` (this respects the packageManager version in package.json; do NOT pin pnpm@latest).\n`
					: "")
			: "") +
		`- CRITICAL: Use a single \`COPY . .\` to copy the entire repository. The build context already contains exactly the git-tracked files. Do NOT use selective COPY commands (e.g. COPY package*.json) — they will break in monorepos and non-standard layouts.\n` +
		`- After COPY, initialize a git repository so that tests relying on git work:\n` +
		`  RUN git init && git remote add origin https://github.com/placeholder/repo.git && git add -A && git commit -m "init" --allow-empty\n` +
		`  This is required because the build context does not include the .git directory.\n` +
		(Object.keys(collectedCredentials).length > 0
			? `The following credentials have been collected and will be passed as Docker build args:\n` +
				Object.keys(collectedCredentials)
					.map((k) => `  - ${k}`)
					.join("\n") +
				`\nUse ARG ${Object.keys(collectedCredentials).join(" \\\n    ARG ")} in the Dockerfile to receive them, and use them to authenticate private dependencies.\n\n`
			: "") +
		`- Do NOT include the test command itself in the Dockerfile.\n` +
		`- Output ONLY valid Dockerfile content. No markdown, no explanation, no code fences.`
	);
}

function buildDockerfileFixPrompt(
	dockerfileContent: string,
	errorOutput: string,
	testCommand: string,
	context: string,
	packageManager: PackageManager | null,
): string {
	return (
		`The following Dockerfile failed when building or running tests.\n\n` +
		`Test command: ${testCommand}\n\n` +
		(packageManager
			? `Package manager: ${packageManager.name} (lockfile: ${packageManager.lockfile})\n` +
				`Install command: ${packageManager.installCommand}\n\n`
			: "") +
		`Repository context:\n${context}\n\n` +
		`Current Dockerfile:\n${dockerfileContent}\n\n` +
		`Error output:\n${errorOutput.slice(0, 3000)}\n\n` +
		`Fix the Dockerfile to resolve the error.\n` +
		`CRITICAL: Use a single \`COPY . .\` to copy the entire repository — do NOT use selective COPY commands like \`COPY package*.json\`.\n` +
		`After COPY, ensure a git repository is initialized: RUN git init && git remote add origin https://github.com/placeholder/repo.git && git add -A && git commit -m "init" --allow-empty\n` +
		`Output ONLY valid Dockerfile content. No markdown, no explanation, no code fences.`
	);
}

async function askClaudeToFixDockerfile(
	client: Anthropic,
	dockerfileContent: string,
	errorOutput: string,
	testCommand: string,
	context: string,
	packageManager: PackageManager | null,
): Promise<string | null> {
	const response = await client.messages.create({
		model: DEFAULT_MODEL,
		max_tokens: 1024,
		messages: [
			{
				role: "user",
				content: buildDockerfileFixPrompt(
					dockerfileContent,
					errorOutput,
					testCommand,
					context,
					packageManager,
				),
			},
		],
	});
	const block = response.content.find((b) => b.type === "text");
	return block?.type === "text" ? block.text.trim() : null;
}

async function identifyBaseImage(
	client: Anthropic,
	context: string,
	testCommand: string,
): Promise<string | null> {
	const response = await client.messages.create({
		model: DEFAULT_MODEL,
		max_tokens: 64,
		messages: [{ role: "user", content: buildBaseImagePrompt(context, testCommand) }],
	});

	const block = response.content.find((b) => b.type === "text");
	const text = block?.type === "text" ? block.text.trim() : "";
	const stripped = text
		.replace(/^```[a-z]*\n?/i, "")
		.replace(/\n?```$/, "")
		.trim();

	try {
		const parsed = JSON.parse(stripped) as { repository?: string };
		return parsed.repository ?? null;
	} catch {
		return null;
	}
}

async function fetchDockerHubTags(repository: string): Promise<string[]> {
	const [namespace, image] = repository.includes("/")
		? repository.split("/", 2)
		: ["library", repository];

	const tags: string[] = [];
	let url: string | null =
		`https://hub.docker.com/v2/repositories/${namespace}/${image}/tags/?page_size=100`;

	// Fetch up to 3 pages so Claude sees a broad, representative set
	for (let page = 0; page < 3 && url !== null; page++) {
		const response = await fetch(url);
		if (!response.ok) break;

		const data = (await response.json()) as {
			next?: string | null;
			results?: { name: string }[];
		};

		for (const r of data.results ?? []) {
			// Skip digest-only and other non-version tags
			if (!r.name.startsWith("sha256:") && r.name !== "latest") {
				tags.push(r.name);
			}
		}

		url = data.next ?? null;
	}

	return tags;
}

async function identifyRequiredCredentials(
	client: Anthropic,
	context: string,
	existingDockerfiles: string,
): Promise<RequiredCredential[]> {
	const response = await client.messages.create({
		model: DEFAULT_MODEL,
		max_tokens: 512,
		messages: [{ role: "user", content: buildCredentialsPrompt(context, existingDockerfiles) }],
	});

	const block = response.content.find((b) => b.type === "text");
	const text = block?.type === "text" ? block.text.trim() : "[]";

	// Strip markdown fences if the model wrapped the JSON anyway
	const stripped = text
		.replace(/^```[a-z]*\n?/i, "")
		.replace(/\n?```$/, "")
		.trim();

	try {
		const parsed = JSON.parse(stripped);
		return Array.isArray(parsed) ? (parsed as RequiredCredential[]) : [];
	} catch {
		return [];
	}
}

export interface SandboxPrepareOptions {
	dockerSudo: boolean;
}

export async function runSandboxPrepare(options: SandboxPrepareOptions): Promise<CommandResult> {
	console.log("preparing...");

	const cwd = process.cwd();

	if (!isGitRepo(cwd)) {
		printError("Not a git repository.", undefined, "Run this command from inside a git repo.");
		return { exitCode: 1 };
	}

	const apiKey = process.env.ANTHROPIC_API_KEY;
	if (!apiKey) {
		printError(
			"ANTHROPIC_API_KEY is not set.",
			undefined,
			"Set it to use Claude for test command detection.",
		);
		return { exitCode: 1 };
	}

	const context = gatherRepoContext(cwd);
	const existingDockerfiles = gatherExistingDockerfiles(cwd);
	const packageManager = detectPackageManager(cwd);
	if (packageManager) {
		console.log(`detected package manager: ${packageManager.name} (${packageManager.lockfile})`);
	}

	const client = new Anthropic({ apiKey });

	console.log("scanning for private dependencies...");
	const requiredCredentials = await identifyRequiredCredentials(
		client,
		context,
		existingDockerfiles,
	);

	const collectedCredentials: Record<string, string> = {};
	if (requiredCredentials.length > 0) {
		console.log(`\nFound ${requiredCredentials.length} credential(s) needed:\n`);
		for (const cred of requiredCredentials) {
			console.log(`  ${bold(cred.buildArg)}: ${cred.description}`);
		}
		console.log("");
		for (const cred of requiredCredentials) {
			const value = await promptInput(`${cred.buildArg}: `, { hidden: cred.sensitive });
			collectedCredentials[cred.buildArg] = value;
		}
		console.log("");
	}

	const response = await client.messages.create({
		model: DEFAULT_MODEL,
		max_tokens: 256,
		messages: [{ role: "user", content: buildTestCommandPrompt(context, packageManager) }],
	});

	const block = response.content.find((b) => b.type === "text");
	const testCommand = block?.type === "text" ? block.text.trim() : null;

	if (!testCommand) {
		printError("Could not determine test command.");
		return { exitCode: 1 };
	}

	console.log(`test command: ${testCommand}`);

	console.log("resolving base image tags...");
	const baseImageRepo = await identifyBaseImage(client, context, testCommand);
	const availableTags = baseImageRepo ? await fetchDockerHubTags(baseImageRepo) : [];
	if (baseImageRepo) {
		console.log(`  ${baseImageRepo}: ${availableTags.length} tags fetched from Docker Hub`);
	}

	console.log("generating Dockerfile...");

	const dockerfileResponse = await client.messages.create({
		model: DEFAULT_MODEL,
		max_tokens: 1024,
		messages: [
			{
				role: "user",
				content: buildDockerfilePrompt(
					testCommand,
					context,
					existingDockerfiles,
					baseImageRepo,
					availableTags,
					collectedCredentials,
					packageManager,
				),
			},
		],
	});

	const dockerfileBlock = dockerfileResponse.content.find((b) => b.type === "text");
	const dockerfileContent = dockerfileBlock?.type === "text" ? dockerfileBlock.text.trim() : null;

	if (!dockerfileContent) {
		printError("Could not generate Dockerfile.");
		return { exitCode: 1 };
	}

	let dockerfileName = "Dockerfile.chunk";
	let counter = 1;
	while (existsSync(join(cwd, dockerfileName))) {
		dockerfileName = `Dockerfile.chunk.${counter++}`;
	}
	const dockerfilePath = join(cwd, dockerfileName);
	let currentDockerfileContent = dockerfileContent;
	writeFileSync(dockerfilePath, `${currentDockerfileContent}\n`, "utf-8");
	console.log(`wrote ${dockerfileName}`);

	const imageTag = "chunk-prep";
	const dockerCmd = options.dockerSudo ? "sudo" : "docker";
	const dockerArgs = (subcmd: string[]) => (options.dockerSudo ? ["docker", ...subcmd] : subcmd);

	const buildArgs = Object.entries(collectedCredentials).flatMap(([k, v]) => [
		"--build-arg",
		`${k}=${v}`,
	]);

	const MAX_RETRIES = 3;
	// Build context: only files tracked by git (respects .gitignore, excludes .git dir)
	const buildContext = mkdtempSync(join(tmpdir(), "chunk-build-"));
	let prepSuccess = false;

	try {
		try {
			const archive = execFileSync("git", ["archive", "HEAD"], { cwd });
			execFileSync("tar", ["-x", "-C", buildContext], { input: archive });
		} catch {
			printError("Failed to prepare build context.");
			return { exitCode: 1 };
		}

		for (let attempt = 0; attempt <= MAX_RETRIES; attempt++) {
			if (attempt > 0) {
				console.log(`\nfixing Dockerfile (attempt ${attempt} of ${MAX_RETRIES})...`);
				writeFileSync(dockerfilePath, `${currentDockerfileContent}\n`, "utf-8");
			}
			writeFileSync(join(buildContext, dockerfileName), `${currentDockerfileContent}\n`, "utf-8");

			console.log(`\nbuilding ${dockerfileName}...`);
			let buildErrorOutput: string | null = null;
			try {
				const out = execFileSync(
					dockerCmd,
					dockerArgs(["build", "-f", dockerfileName, "-t", imageTag, ...buildArgs, "."]),
					{ cwd: buildContext, stdio: "pipe" },
				);
				process.stdout.write(out);
			} catch (err) {
				const e = err as Error & { stdout?: Buffer; stderr?: Buffer };
				const stdout = e.stdout?.toString() ?? "";
				const stderr = e.stderr?.toString() ?? "";
				if (stdout) process.stdout.write(stdout);
				if (stderr) process.stderr.write(stderr);
				buildErrorOutput = `${stdout}\n${stderr}`.trim();
			}

			if (buildErrorOutput !== null) {
				if (attempt < MAX_RETRIES) {
					console.log("Docker build failed. Asking Claude to fix the Dockerfile...");
					const fixed = await askClaudeToFixDockerfile(
						client,
						currentDockerfileContent,
						buildErrorOutput,
						testCommand,
						context,
						packageManager,
					);
					if (fixed) currentDockerfileContent = fixed;
					continue;
				}
				printError("Docker build failed.", undefined, `Check ${dockerfileName} for issues.`);
				break;
			}

			console.log(`\nrunning test command in container...`);
			let runErrorOutput: string | null = null;
			try {
				const out = execFileSync(
					dockerCmd,
					dockerArgs(["run", "--rm", imageTag, "sh", "-c", testCommand]),
					{ cwd, stdio: "pipe" },
				);
				process.stdout.write(out);
				prepSuccess = true;
				break;
			} catch (err) {
				const e = err as Error & { stdout?: Buffer; stderr?: Buffer };
				const stdout = e.stdout?.toString() ?? "";
				const stderr = e.stderr?.toString() ?? "";
				if (stdout) process.stdout.write(stdout);
				if (stderr) process.stderr.write(stderr);
				runErrorOutput = `${stdout}\n${stderr}`.trim();
			}

			if (attempt < MAX_RETRIES) {
				console.log("Tests failed. Asking Claude to fix the Dockerfile...");
				const fixed = await askClaudeToFixDockerfile(
					client,
					currentDockerfileContent,
					// biome-ignore lint/style/noNonNullAssertion: runErrorOutput is set in the catch block above
					runErrorOutput!,
					testCommand,
					context,
					packageManager,
				);
				if (fixed) currentDockerfileContent = fixed;
			} else {
				printError("Tests failed inside the container.");
			}
		}
	} finally {
		rmSync(buildContext, { recursive: true, force: true });
	}

	return { exitCode: prepSuccess ? 0 : 1 };
}
