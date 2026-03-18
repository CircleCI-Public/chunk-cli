import Anthropic from "@anthropic-ai/sdk";
import { DEFAULT_MODEL } from "../../config";
import {
	buildTestCommandPrompt,
	detectPackageManager,
	gatherRepoContext,
	isGitRepo,
} from "../../core/validate.steps";
import { resolveConfig } from "../../storage/config";
import {
	getProjectConfigPath,
	loadProjectConfig,
	saveProjectConfig,
} from "../../storage/project-config";
import type { CommandResult } from "../../types";
import { handleError, printError } from "../../utils/errors";

export interface ValidateInitOptions {
	force: boolean;
}

export async function runValidateInit(options: ValidateInitOptions): Promise<CommandResult> {
	const cwd = process.cwd();

	if (!isGitRepo(cwd)) {
		printError("Not a git repository.", undefined, "Run this command from inside a git repo.");
		return { exitCode: 1 };
	}

	const configPath = getProjectConfigPath();
	if (!options.force && loadProjectConfig().testCommand) {
		console.log(`Config already exists at ${configPath}`);
		console.log(`  To view the current config: cat ${configPath}`);
		console.log("  To re-detect and overwrite:  chunk validate init --force");
		return { exitCode: 0 };
	}

	const { apiKey } = resolveConfig();
	if (!apiKey) {
		printError("No API key found.", undefined, "Set ANTHROPIC_API_KEY or run `chunk auth login`.");
		return { exitCode: 1 };
	}

	const packageManager = detectPackageManager(cwd);
	if (packageManager) {
		console.log(`detected package manager: ${packageManager.name} (${packageManager.lockfile})`);
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

	try {
		saveProjectConfig({
			...(packageManager ? { installCommand: packageManager.installCommand } : {}),
			testCommand,
		});
	} catch (err) {
		handleError(err, { brief: `Failed to write config to ${configPath}.` });
		return { exitCode: 1 };
	}

	console.log(`wrote ${configPath}`);

	return { exitCode: 0 };
}
