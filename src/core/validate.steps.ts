import { execFileSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { parse as parseYaml } from "yaml";

// ---------------------------------------------------------------------------
// Validate commands (hook config)
// ---------------------------------------------------------------------------

interface HookConfig {
	execs?: Record<string, { command?: string }>;
}

export type LoadValidateCommandsResult = { commands: string[] } | { commands: []; error: string };

export function loadValidateCommands(projectDir: string): LoadValidateCommandsResult {
	const configPath = join(projectDir, ".chunk", "hook", "config.yml");
	if (!existsSync(configPath)) return { commands: [] };
	try {
		const content = readFileSync(configPath, "utf-8");
		const config = parseYaml(content) as HookConfig;
		if (!config.execs) return { commands: [] };
		const commands = Object.values(config.execs)
			.map((exec) => exec.command ?? "")
			.filter((cmd) => cmd.length > 0 && !cmd.includes("{{"));
		return { commands };
	} catch (e) {
		return { commands: [], error: `Failed to parse ${configPath}: ${e}` };
	}
}

// ---------------------------------------------------------------------------
// Package manager and repo context detection
// ---------------------------------------------------------------------------

export interface PackageManager {
	name: string;
	installCommand: string;
	lockfile: string;
}

export function isGitRepo(cwd: string): boolean {
	try {
		execFileSync("git", ["rev-parse", "--git-dir"], { cwd, stdio: "ignore" });
		return true;
	} catch {
		return false;
	}
}

export function detectPackageManager(cwd: string): PackageManager | null {
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

export function gatherRepoContext(cwd: string): string {
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

export function buildTestCommandPrompt(
	context: string,
	packageManager: PackageManager | null,
): string {
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
