/**
 * `chunk hook` — Hook management commands for AI coding agent integrations.
 *
 * Provides exec, task, sync, state, scope, repo, and env commands for Claude Code hooks.
 * This module exports `registerHookCommands` which is called from the main
 * chunk CLI entry point to register the `hook` command group.
 */

import { basename, resolve } from "node:path";
import type { Command } from "@commander-js/extra-typings";
import { type ExecFlags, runExec } from "./commands/exec";
import { activateScope, deactivateScope } from "./commands/scope";
import { runState, type StateFlags } from "./commands/state";
import { parseSpecs, runSync, type SyncFlags } from "./commands/sync";
import { runTask, type TaskFlags } from "./commands/task";
import { getAdapter } from "./lib/adapter";
import { extractSessionId } from "./lib/compat";
import { loadConfig } from "./lib/config";
import type { Subcommand } from "./lib/env";
import { isEnabled, resolveProject } from "./lib/env";
import { buildEnvUpdateOptions, runEnvUpdate } from "./lib/env-update";
import { initLog, log } from "./lib/log";
import { type CopyResult, runRepoInit } from "./lib/repo-init";
import { runHookSetup } from "./lib/setup";
import { PROFILES } from "./lib/shell-env";

const TAG = "cli";

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

function parseInt10(value: string): number {
	return parseInt(value, 10);
}

// ---------------------------------------------------------------------------
// Public API — register hook subcommands under a parent Commander command
// ---------------------------------------------------------------------------

export type { EnvUpdateResult } from "./lib/env-update";
export type { CopyResult } from "./lib/repo-init";
export type { HookSetupOptions, HookSetupResult } from "./lib/setup";
/**
 * Register all hook subcommands under the given parent command.
 *
 * Usage (in chunk CLI entry point):
 *   const hook = program.command("hook").description("...");
 *   registerHookCommands(hook);
 */
export { runHookSetup } from "./lib/setup";
export type { Profile } from "./lib/shell-env";
export { PROFILES } from "./lib/shell-env";

export function registerHookCommands(parent: Command): void {
	registerExec(parent);
	registerTask(parent);
	registerSync(parent);
	registerState(parent);
	registerScope(parent);
	registerRepo(parent);
	registerEnv(parent);
	registerSetup(parent);
}

// ---------------------------------------------------------------------------
// exec
// ---------------------------------------------------------------------------

function registerExec(parent: Command): void {
	const exec = parent
		.command("exec")
		.description("Execute a shell command and enforce the result (test, lint, etc.)");

	// exec run <name>
	exec
		.command("run")
		.description("Run command, save result, check result")
		.argument("<name>", "Exec name (matches config key)")
		.option("--cmd <command>", "Override exec command")
		.option("--timeout <seconds>", "Override timeout in seconds", parseInt10)
		.option("--file-ext <ext>", "File extension filter (e.g. .go)")
		.option("--staged", "Only consider staged files", false)
		.option("--always", "Run even if no matching files changed", false)
		.option("--on <trigger>", "Named trigger group from config")
		.option("--trigger <pattern>", "Inline trigger pattern")
		.option("--no-check", "Save result for deferred check (always exits 0)")
		.option("--limit <n>", "Max consecutive blocks before auto-allowing", parseInt10)
		.option("--project <path>", "Override project directory")
		.option("--matcher <pattern>", "Tool-name regex filter")
		.action(async (name, opts) => {
			const adapter = getAdapter();

			if (!isEnabled(name)) {
				log(TAG, `Exec "${name}" not enabled, allowing`);
				adapter.allow();
			}

			const isNoCheck = !opts.check;

			const event = isNoCheck ? { eventName: "", raw: {} } : await adapter.readEvent();

			const projectDir = resolveProject(opts.project, event.cwd);
			const config = loadConfig(projectDir);
			initLog({ projectDir });
			log(TAG, `command=exec subcommand=run name=${name} project=${config.projectDir}`);

			// Multi-repo scope gate (skip for --no-check).
			// Runs before --matcher so every tool call can activate scope.
			if (!isNoCheck) {
				const sessionId = extractSessionId(event.raw);
				if (!activateScope(config.projectDir, event.raw, sessionId)) {
					log(TAG, `Scope not active for "${config.projectDir}", allowing`);
					adapter.allow();
				}
			}

			// Matcher filter
			if (opts.matcher && event.toolName) {
				const re = new RegExp(opts.matcher);
				if (!re.test(event.toolName)) {
					log(TAG, `Tool "${event.toolName}" does not match --matcher, allowing`);
					adapter.allow();
				}
			}

			const flags: ExecFlags = {
				subcommand: "run",
				name,
				cmd: opts.cmd,
				timeout: opts.timeout,
				fileExt: opts.fileExt,
				staged: opts.staged,
				always: opts.always,
				noCheck: isNoCheck,
				limit: opts.limit,
				on: opts.on,
				trigger: opts.trigger,
			};

			return runExec(config, adapter, event, flags);
		});

	// exec check <name>
	exec
		.command("check")
		.description("Check a previously saved result")
		.argument("<name>", "Exec name (matches config key)")
		.option("--file-ext <ext>", "File extension filter (e.g. .go)")
		.option("--staged", "Only consider staged files", false)
		.option("--always", "Run even if no matching files changed", false)
		.option("--on <trigger>", "Named trigger group from config")
		.option("--trigger <pattern>", "Inline trigger pattern")
		.option("--matcher <pattern>", "Tool-name regex filter")
		.option("--limit <n>", "Max consecutive blocks before auto-allowing", parseInt10)
		.option("--timeout <seconds>", "Override timeout in seconds", parseInt10)
		.option("--project <path>", "Override project directory")
		.action(async (name, opts) => {
			const adapter = getAdapter();

			if (!isEnabled(name)) {
				log(TAG, `Exec "${name}" not enabled, allowing`);
				adapter.allow();
			}

			const event = await adapter.readEvent();
			const projectDir = resolveProject(opts.project, event.cwd);
			const config = loadConfig(projectDir);
			initLog({ projectDir });
			log(TAG, `command=exec subcommand=check name=${name} project=${config.projectDir}`);

			// Scope gate before matcher so every tool call can activate scope.
			const sessionId = extractSessionId(event.raw);
			if (!activateScope(config.projectDir, event.raw, sessionId)) {
				log(TAG, `Scope not active for "${config.projectDir}", allowing`);
				adapter.allow();
			}

			if (opts.matcher && event.toolName) {
				const re = new RegExp(opts.matcher);
				if (!re.test(event.toolName)) {
					log(TAG, `Tool "${event.toolName}" does not match --matcher, allowing`);
					adapter.allow();
				}
			}

			const flags: ExecFlags = {
				subcommand: "check",
				name,
				fileExt: opts.fileExt,
				staged: opts.staged,
				always: opts.always,
				on: opts.on,
				trigger: opts.trigger,
				limit: opts.limit,
				timeout: opts.timeout,
			};

			return runExec(config, adapter, event, flags);
		});
}

// ---------------------------------------------------------------------------
// task
// ---------------------------------------------------------------------------

function registerTask(parent: Command): void {
	const task = parent
		.command("task")
		.description("Delegate a task to a subagent and enforce the result (code review, etc.)");

	// task check <name>
	task
		.command("check")
		.description("Check a previously saved task result")
		.argument("<name>", "Task name (matches config key)")
		.option("--instructions <path>", "Path to task instructions file")
		.option("--schema <path>", "Path to custom result schema file")
		.option("--always", "Run even if no files changed", false)
		.option("--staged", "Only consider staged files", false)
		.option("--on <trigger>", "Named trigger group from config")
		.option("--trigger <pattern>", "Inline trigger pattern")
		.option("--matcher <pattern>", "Tool-name regex filter")
		.option("--limit <n>", "Max consecutive blocks before auto-allowing", parseInt10)
		.option("--project <path>", "Override project directory")
		.action(async (name, opts) => {
			const adapter = getAdapter();

			if (!isEnabled(name)) {
				log(TAG, `Task "${name}" not enabled, allowing`);
				adapter.allow();
			}

			const event = await adapter.readEvent();
			const projectDir = resolveProject(opts.project, event.cwd);
			const config = loadConfig(projectDir);
			initLog({ projectDir });
			log(TAG, `command=task subcommand=check name=${name} project=${config.projectDir}`);

			// Scope gate before matcher so every tool call can activate scope.
			const sessionId = extractSessionId(event.raw);
			if (!activateScope(config.projectDir, event.raw, sessionId)) {
				log(TAG, `Scope not active for "${config.projectDir}", allowing`);
				adapter.allow();
			}

			if (opts.matcher && event.toolName) {
				const re = new RegExp(opts.matcher);
				if (!re.test(event.toolName)) {
					log(TAG, `Tool "${event.toolName}" does not match --matcher, allowing`);
					adapter.allow();
				}
			}

			const flags: TaskFlags = {
				subcommand: "check" as Subcommand,
				name,
				instructions: opts.instructions,
				schema: opts.schema,
				limit: opts.limit,
				always: opts.always,
				staged: opts.staged,
				on: opts.on,
				trigger: opts.trigger,
			};

			return runTask(config, adapter, event, flags);
		});
}

// ---------------------------------------------------------------------------
// sync
// ---------------------------------------------------------------------------

function registerSync(parent: Command): void {
	const sync = parent
		.command("sync")
		.description("Group multiple exec/task checks into a single sequential check");

	// sync check <specs...>
	sync
		.command("check")
		.description("Check sentinels for a group of commands sequentially")
		.argument("<specs...>", "Command specs: exec:<name> or task:<name>")
		.option("--on <trigger>", "Named trigger group from config")
		.option("--trigger <pattern>", "Inline trigger pattern")
		.option("--matcher <pattern>", "Tool-name regex filter")
		.option("--limit <n>", "Max blocks before auto-allowing", parseInt10)
		.option("--staged", "Pass --staged to delegated commands", false)
		.option("--always", "Pass --always to delegated commands", false)
		.option(
			"--on-fail <mode>",
			'Failure strategy: "restart" (reset all, default) or "retry" (keep passed)',
		)
		.option("--bail", "Stop at first non-pass spec instead of evaluating all", false)
		.option("--project <path>", "Override project directory")
		.action(async (specArgs, opts) => {
			const specs = parseSpecs(specArgs);
			if (!specs) {
				console.error(
					"Invalid command specs. " +
						"Usage: chunk hook sync check exec:<name> [task:<name>...] [flags]",
				);
				process.exit(1);
			}

			const adapter = getAdapter();

			const anyEnabled = specs.some((s) => isEnabled(s.name));
			if (!anyEnabled) {
				log(TAG, "No sync specs enabled, allowing");
				adapter.allow();
			}

			const event = await adapter.readEvent();
			const projectDir = resolveProject(opts.project, event.cwd);
			const config = loadConfig(projectDir);
			initLog({ projectDir });
			log(TAG, `command=sync specs=${specArgs.join(",")} project=${config.projectDir}`);

			// Scope gate before matcher so every tool call can activate scope.
			const sessionId = extractSessionId(event.raw);
			if (!activateScope(config.projectDir, event.raw, sessionId)) {
				log(TAG, `Scope not active for "${config.projectDir}", allowing`);
				adapter.allow();
			}

			if (opts.matcher && event.toolName) {
				const re = new RegExp(opts.matcher);
				if (!re.test(event.toolName)) {
					log(TAG, `Tool "${event.toolName}" does not match --matcher, allowing`);
					adapter.allow();
				}
			}

			const rawOnFail = typeof opts.onFail === "string" ? opts.onFail : undefined;
			const onFail = rawOnFail === "retry" ? ("retry" as const) : ("restart" as const);

			const flags: SyncFlags = {
				subcommand: "check",
				specs,
				on: opts.on,
				trigger: opts.trigger,
				matcher: opts.matcher,
				limit: opts.limit,
				staged: opts.staged,
				always: opts.always,
				onFail,
				bail: opts.bail === true,
			};

			return runSync(config, adapter, event, flags);
		});
}

// ---------------------------------------------------------------------------
// state
// ---------------------------------------------------------------------------

function registerState(parent: Command): void {
	const state = parent
		.command("state")
		.description("Manage per-project state for cross-event data sharing");

	// state save
	state
		.command("save")
		.description("Save event input to state (namespaced by event name)")
		.option("--project <path>", "Override project directory")
		.action(async (opts) => {
			const adapter = getAdapter();
			const event = await adapter.readEvent();
			const projectDir = resolveProject(opts.project, event.cwd);
			const config = loadConfig(projectDir);
			initLog({ projectDir });
			log(TAG, `command=state subcommand=save project=${config.projectDir}`);

			const flags: StateFlags = { subcommand: "save" };
			return runState(config, adapter, event, flags);
		});

	// state append
	state
		.command("append")
		.description("Append event input as a new entry (records HEAD SHA per entry)")
		.option("--project <path>", "Override project directory")
		.action(async (opts) => {
			const adapter = getAdapter();
			const event = await adapter.readEvent();
			const projectDir = resolveProject(opts.project, event.cwd);
			const config = loadConfig(projectDir);
			initLog({ projectDir });
			log(TAG, `command=state subcommand=append project=${config.projectDir}`);

			const flags: StateFlags = { subcommand: "append" };
			return runState(config, adapter, event, flags);
		});

	// state load [field]
	state
		.command("load")
		.description("Load a field (or entire state) and write to stdout")
		.argument("[field]", "Field path (e.g. UserPromptSubmit.prompt)")
		.option("--project <path>", "Override project directory")
		.action(async (field, opts) => {
			const adapter = getAdapter();
			const event = { eventName: "", raw: {} };
			const projectDir = resolveProject(opts.project);
			const config = loadConfig(projectDir);
			initLog({ projectDir });
			log(TAG, `command=state subcommand=load project=${config.projectDir}`);

			const flags: StateFlags = { subcommand: "load", field };
			return runState(config, adapter, event, flags);
		});

	// state clear
	state
		.command("clear")
		.description("Clear all saved state for the project")
		.option("--project <path>", "Override project directory")
		.action(async (opts) => {
			const adapter = getAdapter();
			const event = await adapter.readEvent();
			const projectDir = resolveProject(opts.project, event.cwd);
			const config = loadConfig(projectDir);
			initLog({ projectDir });
			log(TAG, `command=state subcommand=clear project=${config.projectDir}`);

			const flags: StateFlags = { subcommand: "clear" };
			return runState(config, adapter, event, flags);
		});
}

// ---------------------------------------------------------------------------
// scope
// ---------------------------------------------------------------------------

function registerScope(parent: Command): void {
	const scope = parent
		.command("scope")
		.description("Per-repo activity gate for multi-repo workspaces");

	// scope activate
	scope
		.command("activate")
		.description("Activate scope if stdin paths reference the project")
		.option("--project <path>", "Override project directory")
		.action(async (opts) => {
			const projectDir = resolveProject(opts.project);
			initLog({ projectDir });

			let raw: Record<string, unknown> = {};
			try {
				const text = await Bun.stdin.text();
				if (text.trim()) raw = JSON.parse(text);
			} catch {
				// Malformed stdin — no context available
			}

			const sessionId = extractSessionId(raw);
			try {
				activateScope(projectDir, raw, sessionId);
			} catch (err) {
				log(TAG, `scope activate failed: ${err}`);
				console.error(`chunk hook: scope activate failed: ${err}`);
				process.exit(1);
			}
			process.exit(0);
		});

	// scope deactivate
	scope
		.command("deactivate")
		.description("Remove the scope marker file")
		.option("--project <path>", "Override project directory")
		.action(async (opts) => {
			const projectDir = resolveProject(opts.project);
			initLog({ projectDir });

			// Read stdin to extract session ID for session-aware deactivation.
			let raw: Record<string, unknown> = {};
			try {
				const text = await Bun.stdin.text();
				if (text.trim()) raw = JSON.parse(text);
			} catch {
				// Malformed stdin — no context available
			}

			const sessionId = extractSessionId(raw);
			if (!sessionId) {
				log(TAG, "scope deactivate: no session ID — command must be called from a hook context");
				console.error(
					"chunk hook: scope deactivate requires an active session (no session ID in stdin)",
				);
				process.exit(1);
			}
			deactivateScope(projectDir, sessionId);
			process.exit(0);
		});
}

// ---------------------------------------------------------------------------
// repo
// ---------------------------------------------------------------------------

function registerRepo(parent: Command): void {
	const repo = parent.command("repo").description("Repository setup commands");

	// repo init [dir]
	repo
		.command("init")
		.description("Initialize a repo with hook configuration files")
		.argument("[dir]", "Target directory (default: current directory)", ".")
		.option("--force", "Overwrite existing files instead of creating .example copies", false)
		.action((dir, opts) => {
			const results = runRepoInit({
				targetDir: dir,
				force: opts.force,
			});

			const projectName = basename(resolve(dir));

			console.log("");
			for (const r of results) {
				formatCopyResult(r);
			}

			console.log("");
			console.log(`Repo initialized (project: ${projectName}).`);
			console.log("");
			console.log("Next steps:");
			console.log(
				"  1. Edit .chunk/hook/config.yml — set the command: fields for your repo's tools.",
			);
			console.log('     Example (Go):     command: "go test ./..."');
			console.log('     Example (Node):   command: "npm test"');
			console.log('     Example (Python): command: "pytest"');
			console.log("");
			console.log(
				"  2. Review .claude/settings.json — adjust hook matchers and timeouts if needed.",
			);
			console.log("");
			console.log(
				"  3. Review .chunk/hook/code-review-instructions.md — customize the review prompt.",
			);
			console.log("");
			console.log("  4. Ensure chunk is installed: chunk --version");
		});
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

// ---------------------------------------------------------------------------
// setup
// ---------------------------------------------------------------------------

function registerSetup(parent: Command): void {
	parent
		.command("setup")
		.description("One-shot setup: configure shell env and initialize repo hook files")
		.argument("[dir]", "Target directory (default: current directory)", ".")
		.option("--force", "Overwrite existing files instead of creating .example copies", false)
		.option("--skip-env", "Skip the shell environment update step", false)
		.option("--profile <name>", `Environment profile (${PROFILES.join(", ")})`, "enable")
		.action((dir, opts) => {
			const profile = opts.profile as (typeof PROFILES)[number];
			if (!PROFILES.includes(profile)) {
				console.error(`Invalid profile: ${opts.profile}. Valid profiles: ${PROFILES.join(", ")}`);
				process.exit(1);
			}

			const result = runHookSetup({
				targetDir: dir,
				profile,
				force: opts.force,
				skipEnv: opts.skipEnv,
			});

			const projectName = basename(resolve(dir));

			console.log("");
			console.log("Setup complete!");
			console.log("");
			console.log("Config files:");
			for (const r of result.copyResults) {
				formatCopyResult(r);
			}

			if (result.envResult) {
				const env = result.envResult;
				console.log("");
				console.log("Shell environment:");
				if (env.overwritten) {
					console.log(`  ⚠ ENV file overwritten: ${env.envFile}`);
				} else {
					console.log(`  ✓ ENV file created: ${env.envFile}`);
				}
				for (const f of env.startupFiles) {
					console.log(`  ✓ ${f} updated`);
				}
			}

			console.log("");
			console.log("Next steps:");
			console.log("  1. Edit .chunk/hook/config.yml — set command: fields for tests/lint");
			console.log("  2. Edit .chunk/hook/code-review-instructions.md — customize review prompt");
			if (result.envResult) {
				const firstFile = result.envResult.startupFiles[0] ?? "~/.zprofile";
				console.log(`  3. Restart your terminal or: source ${firstFile}`);
			}
			console.log("");
			console.log(`Project: ${projectName}`);
		});
}

// ---------------------------------------------------------------------------
// env
// ---------------------------------------------------------------------------

function registerEnv(parent: Command): void {
	const env = parent.command("env").description("Shell environment configuration");

	// env update
	env
		.command("update")
		.description("Configure CHUNK_HOOK_* environment variables in your shell")
		.option("--profile <name>", `Environment profile (${PROFILES.join(", ")})`, "enable")
		.option("--env-file <path>", "Override ENV file path")
		.option("--set-log-dir <path>", "Log directory to write into the ENV file")
		.option("--set-verbose", "Enable verbose logging in the generated ENV", false)
		.option("--set-project-root <path>", "Multi-repo project root to write into the ENV file")
		.action((opts) => {
			if (!PROFILES.includes(opts.profile as (typeof PROFILES)[number])) {
				console.error(`Invalid profile: ${opts.profile}. Valid profiles: ${PROFILES.join(", ")}`);
				process.exit(1);
			}

			const options = buildEnvUpdateOptions({
				profile: opts.profile,
				envFile: opts.envFile,
				logDir: opts.setLogDir,
				verbose: opts.setVerbose,
				projectRoot: opts.setProjectRoot,
			});

			const result = runEnvUpdate(options);

			console.log("");
			console.log("Configuration complete!");
			console.log("");
			if (result.overwritten) {
				console.log(`  ⚠ ENV file overwritten: ${result.envFile}`);
			} else {
				console.log(`  ✓ ENV file created: ${result.envFile}`);
			}
			console.log(`  ✓ Profile: ${result.profile}`);
			console.log(`  ✓ Log dir: ${result.logDir}`);
			console.log("");
			console.log("Shell startup files updated:");
			for (const f of result.startupFiles) {
				console.log(`  ✓ ${f}`);
			}
			console.log("");
			console.log("Restart your terminal or run:");
			console.log(`  source ${result.startupFiles[0] ?? "~/.zprofile"}`);
			console.log("");
			console.log("Quick toggle (without re-running env update):");
			console.log(`  echo 'export CHUNK_HOOK_ENABLE=0' > ${result.envFile}   # disable all`);
			console.log(`  echo 'export CHUNK_HOOK_ENABLE=1' > ${result.envFile}   # enable all`);
		});
}
