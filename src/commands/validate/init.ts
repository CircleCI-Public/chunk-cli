import { basename, join } from "node:path";
import Anthropic from "@anthropic-ai/sdk";
import type { CopyResult, EnvUpdateResult } from "@chunk/hook";
import { PROFILES, runHookSetup } from "@chunk/hook";
import { DEFAULT_MODEL } from "../../config";
import type { CommandConfig } from "../../core/run-config";
import { configExists, loadRunConfig, saveSequenceConfig } from "../../core/run-config";
import {
	buildTestCommandPrompt,
	detectPackageManager,
	gatherRepoContext,
	isGitRepo,
} from "../../core/validate.steps";
import { resolveConfig } from "../../storage/config";
import type { CommandResult } from "../../types";
import { handleError, printError } from "../../utils/errors";

export interface ValidateInitOptions {
	force: boolean;
	profile: string;
	skipEnv: boolean;
}

function formatCopyResult(r: CopyResult): void {
	switch (r.action) {
		case "created":
			console.log(`  ✓ Created ${r.relativePath}`);
			break;
		case "example": {
			const exName = basename(r.examplePath ?? "");
			console.log(`  ⚠ ${r.relativePath} already exists — saved template as ${exName}`);
			break;
		}
		case "skipped":
			console.log(`  - Skipped ${r.relativePath}`);
			break;
	}
}

export async function runValidateInit(options: ValidateInitOptions): Promise<CommandResult> {
	const cwd = process.cwd();

	if (!PROFILES.includes(options.profile as (typeof PROFILES)[number])) {
		printError(
			`Invalid profile: ${options.profile}`,
			undefined,
			`Valid profiles: ${PROFILES.join(", ")}`,
		);
		return { exitCode: 1 };
	}

	if (!isGitRepo(cwd)) {
		printError("Not a git repository.", undefined, "Run this command from inside a git repo.");
		return { exitCode: 1 };
	}

	const configPath = join(cwd, ".chunk", "commands.json");
	if (!options.force && configExists(cwd)) {
		const existing = loadRunConfig(cwd);
		if (existing.sequence?.length) {
			console.log(`Config already exists at ${configPath}`);
			console.log(`  To view the current config: cat ${configPath}`);
			console.log("  To re-detect and overwrite:  chunk validate:init --force");
			return { exitCode: 0 };
		}
	}

	// Phase 1 — hook setup
	const hookResult = runHookSetup({
		targetDir: cwd,
		profile: options.profile as (typeof PROFILES)[number],
		force: options.force,
		skipEnv: options.skipEnv,
	});

	console.log("\nHook setup:\n");
	for (const r of hookResult.copyResults) {
		formatCopyResult(r);
	}

	if (hookResult.envResult) {
		const env: EnvUpdateResult = hookResult.envResult;
		console.log("\nShell environment:");
		if (env.overwritten) {
			console.log(`  ⚠ ENV file overwritten: ${env.envFile}`);
		} else {
			console.log(`  ✓ ENV file created: ${env.envFile}`);
		}
		for (const f of env.startupFiles) {
			console.log(`  ✓ ${f} updated`);
		}
	}

	// Phase 2 — detect commands
	const { apiKey } = resolveConfig();
	if (!apiKey) {
		printError("No API key found.", undefined, "Set ANTHROPIC_API_KEY or run `chunk auth login`.");
		return { exitCode: 1 };
	}

	const packageManager = detectPackageManager(cwd);
	if (packageManager) {
		console.log(`\ndetected package manager: ${packageManager.name} (${packageManager.lockfile})`);
	}

	const context = gatherRepoContext(cwd);
	const client = new Anthropic({ apiKey });

	console.log("detecting test command...");
	let response: Awaited<ReturnType<typeof client.messages.create>>;
	try {
		response = await client.messages.create({
			model: DEFAULT_MODEL,
			max_tokens: 256,
			messages: [{ role: "user", content: buildTestCommandPrompt(context, packageManager) }],
		});
	} catch (err) {
		handleError(err, { brief: "API request failed." });
		return { exitCode: 1 };
	}

	const block = response.content.find((b) => b.type === "text");
	const testCommand = block?.type === "text" ? block.text.trim() : null;

	if (!testCommand) {
		printError("Could not determine test command.");
		return { exitCode: 1 };
	}

	console.log(`test command: ${testCommand}`);

	const sequence: string[] = [];
	const commands: Record<string, CommandConfig> = {};

	if (packageManager) {
		commands.install = packageManager.installCommand;
		sequence.push("install");
	}
	commands.test = testCommand;
	sequence.push("test");

	try {
		saveSequenceConfig(cwd, sequence, commands);
	} catch (err) {
		handleError(err, { brief: `Failed to write config to ${configPath}.` });
		return { exitCode: 1 };
	}

	console.log(`wrote ${configPath}`);

	// Next-steps footer
	console.log("\nNext steps:");
	console.log("  1. Edit .chunk/hook/config.yml — set command: fields for tests/lint");
	console.log("  2. Edit .chunk/hook/code-review-instructions.md — customize review prompt");
	if (!options.skipEnv && hookResult.envResult) {
		const firstFile = hookResult.envResult.startupFiles[0] ?? "~/.zprofile";
		console.log(`  3. source ${firstFile}`);
	}

	return { exitCode: 0 };
}
