import { validateApiKeyWithServer } from "../core/agent";
import { clearApiKey, loadUserConfig, saveUserConfig } from "../storage/config";
import type { CommandResult, ParsedArgs } from "../types";
import { bold, dim, yellow } from "../ui/colors";
import { label, printSuccess, printWarning } from "../ui/format";
import { promptConfirm, promptInput } from "../ui/prompt";
import { printError } from "../utils/errors";

export function showAuthHelp(): void {
	console.log(`
Usage: chunk auth <command>

Manage authentication

Commands:
  login     Store API key for authentication
  status    Check authentication status
  logout    Remove stored credentials

Options:
  -h, --help    Show this help message

Examples:
  chunk auth login     Interactive API key setup
  chunk auth status    Check if authenticated
  chunk auth logout    Remove credentials
`);
}

async function runAuthLogin(): Promise<CommandResult> {
	console.log(`\n${bold("Chunk CLI - API Key Setup")}\n`);
	console.log("Enter your Anthropic API key (starts with sk-ant-).");
	console.log("The key will be stored securely and never displayed.\n");

	const apiKey = await promptInput("API Key: ", { hidden: true });

	if (!apiKey || apiKey.trim() === "") {
		console.log("");
		printError(
			"API key cannot be empty",
			"You must enter an API key to authenticate.",
			"Get an API key from https://console.anthropic.com/",
		);
		return { exitCode: 2 };
	}

	const trimmedKey = apiKey.trim();

	if (!trimmedKey.startsWith("sk-ant-")) {
		console.log("");
		printError(
			"Invalid API key format",
			'API keys should start with "sk-ant-".',
			"Get a valid API key from https://console.anthropic.com/",
		);
		return { exitCode: 2 };
	}

	console.log(yellow("\nValidating API key..."));

	const isValid = await validateApiKeyWithServer(trimmedKey);

	if (!isValid) {
		console.log("");
		printError(
			"API key validation failed",
			"The API key could not be validated with the Anthropic API.",
			"Check that your key is correct and has not been revoked.",
		);
		return { exitCode: 2 };
	}

	// Save the API key to config file (saveUserConfig handles permissions)
	saveUserConfig({ apiKey: trimmedKey });

	printSuccess("API key validated and saved successfully!");
	console.log(dim("Your API key is stored in ~/.config/chunk/config.json"));
	console.log("You can now run code reviews with: chunk\n");

	return { exitCode: 0 };
}

async function runAuthStatus(): Promise<CommandResult> {
	const userConfig = loadUserConfig();
	const envApiKey = process.env.ANTHROPIC_API_KEY;

	console.log(`\n${bold("Chunk CLI - Authentication Status")}\n`);

	// Check sources of API key
	let apiKey: string | undefined;
	let source: "config" | "env" | undefined;

	if (userConfig.apiKey) {
		apiKey = userConfig.apiKey;
		source = "config";
	} else if (envApiKey) {
		apiKey = envApiKey;
		source = "env";
	}

	if (!apiKey) {
		printWarning("Not authenticated");
		console.log(dim("No API key found in config file or environment."));
		console.log("\nTo authenticate, run: chunk auth login");
		console.log("Or set the ANTHROPIC_API_KEY environment variable.\n");
		return { exitCode: 0 };
	}

	// Show where key is coming from
	const W = 15; // align to "API key source:"
	if (source === "config") {
		console.log(`${label("API key source:", W)} Config file (~/.config/chunk/config.json)`);
	} else if (source === "env") {
		console.log(`${label("API key source:", W)} Environment variable (ANTHROPIC_API_KEY)`);
	}

	// Mask the key (show last 4 chars only)
	const maskedKey = `****${apiKey.slice(-4)}`;
	console.log(`${label("API key:", W)} ${maskedKey}`);

	// Validate the key
	console.log(yellow("\nValidating API key..."));

	const isValid = await validateApiKeyWithServer(apiKey);

	if (isValid) {
		printSuccess("API key is valid");
		return { exitCode: 0 };
	} else {
		console.log("");
		printError(
			"API key validation failed",
			"The API key could not be validated with the Anthropic API.",
			"Run `chunk auth login` to set a new key.",
		);
		return { exitCode: 1 };
	}
}

async function runAuthLogout(): Promise<CommandResult> {
	// Check if there's an API key stored in config
	const userConfig = loadUserConfig();

	if (!userConfig.apiKey) {
		printWarning("No API key stored in config file.");

		if (process.env.ANTHROPIC_API_KEY) {
			console.log("Note: ANTHROPIC_API_KEY is set in your environment variables.");
			console.log("To remove it, unset the environment variable.\n");
		}

		return { exitCode: 0 };
	}

	// Confirm before removing
	console.log("\nThis will remove your stored API key from ~/.config/chunk/config.json");
	const confirmed = await promptConfirm("Are you sure you want to logout?");

	if (!confirmed) {
		console.log("\nLogout cancelled.\n");
		return { exitCode: 0 };
	}

	const success = clearApiKey();

	if (success) {
		printSuccess("API key removed successfully.");
		return { exitCode: 0 };
	} else {
		console.log("");
		printError(
			"Failed to remove API key",
			"An error occurred while trying to remove the API key from the config file.",
			"Check file permissions on ~/.config/chunk/config.json",
		);
		return { exitCode: 2 };
	}
}

export async function runAuth(parsed: ParsedArgs): Promise<CommandResult> {
	if (parsed.flags.help) {
		showAuthHelp();
		return { exitCode: 0 };
	}

	if (!parsed.subcommand) {
		showAuthHelp();
		return { exitCode: 2 };
	}

	const subcommand = parsed.subcommand;

	switch (subcommand) {
		case "login":
			return runAuthLogin();
		case "status":
			return runAuthStatus();
		case "logout":
			return runAuthLogout();
		default:
			printError(
				`Unknown auth command: ${subcommand}`,
				`'${subcommand}' is not a valid auth subcommand.`,
				"Run `chunk auth --help` for available commands.",
			);
			return { exitCode: 2 };
	}
}
