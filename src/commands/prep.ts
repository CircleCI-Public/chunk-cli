import { execSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import Anthropic from "@anthropic-ai/sdk";
import { DEFAULT_MODEL } from "../config";
import type { CommandResult } from "../types";
import { bold } from "../ui/colors";
import { promptInput } from "../ui/prompt";
import { printError } from "../utils/errors";

interface RequiredCredential {
	buildArg: string;
	description: string;
	sensitive: boolean;
}

function isGitRepo(cwd: string): boolean {
	try {
		execSync("git rev-parse --git-dir", { cwd, stdio: "ignore" });
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
			if (
				!/^[Dd]ockerfile(\.[^.]+)?$/.test(entry) ||
				entry.startsWith("Dockerfile.chunk")
			) {
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
	const composeNames = [
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
	];
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

async function identifyBaseImage(
	client: Anthropic,
	context: string,
	testCommand: string,
): Promise<string | null> {
	const response = await client.messages.create({
		model: DEFAULT_MODEL,
		max_tokens: 64,
		messages: [
			{
				role: "user",
				content:
					`You are selecting a Docker base image for a software project.\n\n` +
					`Test command: ${testCommand}\n\n` +
					`Repository context:\n${context}\n\n` +
					`Output ONLY a JSON object with a single field "repository": the Docker Hub repository name ` +
					`for the most appropriate base image (e.g. "clojure", "node", "python", "golang", "rust"). ` +
					`For official images use just the name. For third-party images use "namespace/image". ` +
					`No tag — just the repository name. No explanation, no markdown.`,
			},
		],
	});

	const block = response.content.find((b) => b.type === "text");
	const text = block?.type === "text" ? block.text.trim() : "";
	const stripped = text.replace(/^```[a-z]*\n?/i, "").replace(/\n?```$/, "").trim();

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
		messages: [
			{
				role: "user",
				content:
					`You are analyzing a software repository to identify private or non-public dependencies that require authentication credentials at build or install time.\n\n` +
					`Repository context:\n${context}\n\n` +
					(existingDockerfiles
						? `Existing Dockerfiles:\n${existingDockerfiles}\n\n`
						: "") +
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
					`Output ONLY the JSON array. If nothing private is detected, output [].`,
			},
		],
	});

	const block = response.content.find((b) => b.type === "text");
	const text = block?.type === "text" ? block.text.trim() : "[]";

	// Strip markdown fences if the model wrapped the JSON anyway
	const stripped = text.replace(/^```[a-z]*\n?/i, "").replace(/\n?```$/, "").trim();

	try {
		const parsed = JSON.parse(stripped);
		return Array.isArray(parsed) ? (parsed as RequiredCredential[]) : [];
	} catch {
		return [];
	}
}

export async function runPrep(): Promise<CommandResult> {
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

	const client = new Anthropic({ apiKey });

	console.log("scanning for private dependencies...");
	const requiredCredentials = await identifyRequiredCredentials(client, context, existingDockerfiles);

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
		messages: [
			{
				role: "user",
				content:
					`You are analyzing a software repository to determine how tests are run.\n\n` +
					`${context}\n\n` +
					`Based on the above, output ONLY the shell command used to run the test suite — ` +
					`nothing else. No explanation, no markdown. Just the command string.`,
			},
		],
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
				content:
					`You are generating a Dockerfile to run tests for a software project in a CI environment.\n\n` +
					`Test command: ${testCommand}\n\n` +
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
					(Object.keys(collectedCredentials).length > 0
					? `The following credentials have been collected and will be passed as Docker build args:\n` +
					  Object.keys(collectedCredentials)
					  	.map((k) => `  - ${k}`)
					  	.join("\n") +
					  `\nUse ARG ${Object.keys(collectedCredentials).join(" \\\n    ARG ")} in the Dockerfile to receive them, and use them to authenticate private dependencies.\n\n`
					: "") +
					`- Do NOT include the test command itself in the Dockerfile.\n` +
					`- Output ONLY valid Dockerfile content. No markdown, no explanation, no code fences.`,
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
	writeFileSync(dockerfilePath, `${dockerfileContent}\n`, "utf-8");
	console.log(`wrote ${dockerfileName}`);

	const imageTag = "chunk-prep";

	const buildArgs = Object.entries(collectedCredentials)
		.map(([k, v]) => `--build-arg ${k}=${JSON.stringify(v)}`)
		.join(" ");

	console.log(`\nbuilding ${dockerfileName}...`);
	try {
		execSync(
			`sudo docker build -f ${dockerfileName} -t ${imageTag}${buildArgs ? ` ${buildArgs}` : ""} .`,
			{ cwd, stdio: "inherit" },
		);
	} catch {
		printError("Docker build failed.", undefined, "Check the Dockerfile above for issues.");
		return { exitCode: 1 };
	}

	console.log(`\nrunning test command in container...`);
	try {
		execSync(`sudo docker run --rm ${imageTag} sh -c ${JSON.stringify(testCommand)}`, {
			cwd,
			stdio: "inherit",
		});
	} catch {
		printError("Tests failed inside the container.");
		return { exitCode: 1 };
	}

	return { exitCode: 0 };
}
