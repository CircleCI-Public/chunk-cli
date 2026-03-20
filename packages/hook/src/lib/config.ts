/**
 * Per-repo YAML configuration loader.
 *
 * Reads `.chunk/hook/config.yml` from the project root (or the path
 * specified by `CHUNK_HOOK_CONFIG`). Env vars always win over YAML values.
 */

import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { parse as parseYaml } from "yaml";

import { getEnvConfigPath, getEnvSentinelDir, getEnvTimeout, getProjectDir } from "./env";

// ---------------------------------------------------------------------------
// Raw YAML config types
// ---------------------------------------------------------------------------

/** Hook event routing configuration. */
export type HookEventConfig = {
	/** Tool-name regex filter (e.g. "Bash|Shell|run_in_terminal"). */
	matcher?: string;
	/** Named trigger group from `triggers:`. */
	trigger?: string;
	/** List of command/task names to run as checks. */
	checks?: string[];
};

/** Top-level config shape matching `.chunk/config.yml` (or `.chunk/hook/config.yml`). */
export type Config = {
	triggers?: Record<string, string[]>;
	/** Named commands — canonical key. */
	commands?: Record<string, ExecConfig>;
	/** Backwards-compat alias for `commands:`. */
	execs?: Record<string, ExecConfig>;
	tasks?: Record<string, TaskConfig>;
	sentinels?: { dir?: string };
	/** Hook event routing (only used by `chunk hook run`). */
	hooks?: Record<string, HookEventConfig>;
};

/** Per-exec configuration in the YAML `execs:` section. */
export type ExecConfig = {
	command?: string;
	fileExt?: string;
	/** When true, run even if no matching files changed. Default: false. */
	always?: boolean;
	timeout?: number;
	/** Max consecutive check-blocks before auto-allowing. 0 = unlimited. */
	limit?: number;
};

/** Task command configuration. */
export type TaskConfig = {
	instructions?: string;
	/** Path to a file containing the result schema shown to the agent. */
	schema?: string;
	limit?: number;
	/** When true, run task even if no files changed. Default: false. */
	always?: boolean;
	/**
	 * Maximum seconds a task may remain in "pending" state before the
	 * check treats it as timed out.  Default: 600 (10 minutes).
	 */
	timeout?: number;
};

// ---------------------------------------------------------------------------
// Resolved (merged) config
// ---------------------------------------------------------------------------

/** A single resolved exec definition. */
export type ResolvedExec = {
	command: string;
	fileExt: string;
	always: boolean;
	timeout: number;
	/** Max consecutive check-blocks before auto-allowing. 0 = unlimited. */
	limit: number;
};

/** Resolved (merged) configuration ready for use by commands. */
export type ResolvedConfig = {
	triggers: Record<string, string[]>;
	execs: Record<string, ResolvedExec>;
	tasks: Record<string, Required<TaskConfig>>;
	sentinelDir: string;
	projectDir: string;
	/** Hook event routing (only used by `chunk hook run`). */
	hooks: Record<string, HookEventConfig>;
};

// ---------------------------------------------------------------------------
// Built-in defaults
// ---------------------------------------------------------------------------

const DEFAULT_SENTINEL_DIR = join(process.env.TMPDIR ?? "/tmp", "chunk-hook", "sentinels");

const DEFAULT_TIMEOUT = 300;
const DEFAULT_TASK_TIMEOUT = 600;

/** Built-in trigger groups shipped with the CLI. */
const BUILTIN_TRIGGERS: Record<string, string[]> = {
	"pre-commit": ["git commit", "git push"],
};

// ---------------------------------------------------------------------------
// Loader
// ---------------------------------------------------------------------------

/**
 * Load and resolve config by merging YAML file values with env overrides.
 *
 * Env always wins. Unset values fall back to sensible defaults.
 *
 * @param projectDir – explicit project root (e.g. from hook payload `cwd`).
 *   Falls back to `CLAUDE_PROJECT_DIR` → `process.cwd()`.
 */
export function loadConfig(projectDir?: string, overrides?: Partial<Config>): ResolvedConfig {
	const resolvedProjectDir = projectDir ?? getProjectDir();
	const raw = readConfigFile(resolvedProjectDir);
	const merged: Config = { ...raw, ...overrides };

	// Merge triggers: built-ins + user-defined (user wins on conflict)
	const triggers: Record<string, string[]> = {
		...BUILTIN_TRIGGERS,
		...merged.triggers,
	};

	// Merge commands: `commands:` is canonical, `execs:` is backwards-compat alias.
	// `commands:` wins over `execs:` on key conflict.
	const rawExecs: Record<string, ExecConfig> = {
		...merged.execs,
		...merged.commands,
	};

	// Resolve each exec
	const execs: Record<string, ResolvedExec> = {};
	for (const [name, cfg] of Object.entries(rawExecs)) {
		execs[name] = resolveExec(name, cfg);
	}

	// Resolve each task
	const tasks: Record<string, Required<TaskConfig>> = {};
	if (merged.tasks) {
		for (const [name, cfg] of Object.entries(merged.tasks)) {
			tasks[name] = resolveTask(cfg);
		}
	}

	const sentinelDir = getEnvSentinelDir() ?? merged.sentinels?.dir ?? DEFAULT_SENTINEL_DIR;

	return {
		triggers,
		execs,
		tasks,
		sentinelDir,
		projectDir: resolvedProjectDir,
		hooks: merged.hooks ?? {},
	};
}

/** Resolve a single exec config with defaults. */
function resolveExec(name: string, cfg: ExecConfig): ResolvedExec {
	return {
		command: cfg.command ?? `echo 'No command configured for exec: ${name}'`,
		fileExt: cfg.fileExt ?? "",
		always: cfg.always ?? false,
		timeout: getEnvTimeout(name) ?? cfg.timeout ?? DEFAULT_TIMEOUT,
		limit: cfg.limit ?? 0,
	};
}

/** Resolve a single task config with defaults. */
function resolveTask(cfg: TaskConfig): Required<TaskConfig> {
	return {
		instructions: cfg.instructions ?? "",
		schema: cfg.schema ?? "",
		limit: cfg.limit ?? 3,
		always: cfg.always ?? false,
		timeout: cfg.timeout ?? DEFAULT_TASK_TIMEOUT,
	};
}

/**
 * Look up an exec by name. Returns `undefined` if not defined in config.
 */
export function getExec(config: ResolvedConfig, name: string): ResolvedExec | undefined {
	return config.execs[name];
}

/**
 * Look up a task by name. Returns `undefined` if not defined in config.
 */
export function getTask(config: ResolvedConfig, name: string): Required<TaskConfig> | undefined {
	return config.tasks[name];
}

/**
 * Resolve trigger patterns for a given trigger group name.
 * Returns `undefined` if the trigger group doesn't exist.
 */
export function getTriggerPatterns(
	config: ResolvedConfig,
	triggerName: string,
): string[] | undefined {
	return config.triggers[triggerName];
}

/**
 * Resolve the config file path.
 *
 * Priority:
 *   1. `CHUNK_HOOK_CONFIG` env var
 *   2. `.chunk/config.yml` (new unified location)
 *   3. `.chunk/hook/config.yml` (backwards-compat fallback)
 */
export function resolveConfigPath(projectDir: string): string | undefined {
	const envPath = getEnvConfigPath();
	if (envPath) return existsSync(envPath) ? envPath : undefined;

	const unified = join(projectDir, ".chunk", "config.yml");
	if (existsSync(unified)) return unified;

	const legacy = join(projectDir, ".chunk", "hook", "config.yml");
	if (existsSync(legacy)) return legacy;

	return undefined;
}

/** Read and parse the YAML config file. Returns empty config on any error. */
function readConfigFile(projectDir: string): Config {
	const configPath = resolveConfigPath(projectDir);
	if (!configPath) return {};
	try {
		const content = readFileSync(configPath, "utf-8");
		return (parseYaml(content) as Config) ?? {};
	} catch {
		return {};
	}
}
