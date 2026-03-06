import { execSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import Anthropic from "@anthropic-ai/sdk";
import { DEFAULT_MODEL } from "../config";
import type { CommandResult } from "../types";
import { printError } from "../utils/errors";

function isGitRepo(cwd: string): boolean {
	try {
		execSync("git rev-parse --git-dir", { cwd, stdio: "ignore" });
		return true;
	} catch {
		return false;
	}
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

	// Key config files that signal how tests are run
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

	const client = new Anthropic({ apiKey });
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
					`Requirements:\n` +
					`- Use a CircleCI convenience image (cimg/*) from Docker Hub as the base image.\n` +
					`  Choose the most appropriate one for the detected language and tooling (e.g. cimg/node, cimg/python, cimg/go, cimg/rust, cimg/java, cimg/ruby).\n` +
					`  Pin a specific version tag — do not use "latest".\n` +
					`- Install any additional system-level dependencies needed to run the test command.\n` +
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

	console.log(`\nbuilding ${dockerfileName}...`);
	try {
		execSync(`sudo docker build -f ${dockerfileName} -t ${imageTag} .`, { cwd, stdio: "inherit" });
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
