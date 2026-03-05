/**
 * Shared check-path helpers.
 *
 * Both `exec check` and `task check` follow the same state machine:
 *   1. Stop-event recursion guard → skip if already in a stop continuation
 *   2. Consume sentinel → missing / pending / pass / fail
 */

import type { AgentEvent, HookAdapter } from "./adapter";
import type { ResolvedConfig } from "./config";
import { getTriggerPatterns } from "./config";
import { log } from "./log";
import type { SentinelData } from "./sentinel";
import { incrementBlockCount, resetBlockCount } from "./sentinel";

// ---------------------------------------------------------------------------
// Sentinel evaluation result
// ---------------------------------------------------------------------------

/** Discriminated union describing the outcome of evaluating a sentinel. */
export type SentinelCheckResult =
	| { kind: "missing" }
	| { kind: "pending" }
	| { kind: "pass" }
	| { kind: "fail"; sentinel: SentinelData };

/**
 * Evaluate a consumed sentinel and return a structured result.
 *
 * Staleness checks (applied in order):
 *
 * 1. **Session-aware:** When `currentSessionId` is provided, the sentinel must
 *    carry a matching `sessionId` — otherwise it is treated as stale (`"missing"`).
 *
 * 2. **Content-aware:** When `currentContentHash` is provided, sentinels must
 *    also carry a matching `contentHash` — otherwise they are treated as stale.
 *    This prevents bait-and-switch: an agent cannot run tests on clean code,
 *    then modify files and commit with the stale passing sentinel. Sentinels
 *    without a `contentHash` are also rejected.
 */
export function evaluateSentinel(
	sentinel: SentinelData | undefined,
	currentSessionId?: string,
	currentContentHash?: string,
): SentinelCheckResult {
	if (!sentinel) return { kind: "missing" };

	// Session-aware staleness: the sentinel must belong to the current session.
	// Missing or mismatched sessionId → treat as stale.
	if (currentSessionId && (!sentinel.sessionId || sentinel.sessionId !== currentSessionId)) {
		return { kind: "missing" };
	}

	// Pending sentinels are written before the command finishes, so they
	// never carry a contentHash — check them before hash validation.
	if (sentinel.status === "pending") return { kind: "pending" };

	// Content-aware staleness: if the caller provides a content hash,
	// the sentinel must also have one and it must match. A sentinel
	// without a contentHash is treated as stale — this closes the
	// loophole where a hand-crafted sentinel without a hash bypasses
	// validation entirely. Only applies to terminal states (pass/fail).
	if (currentContentHash) {
		if (!sentinel.contentHash || sentinel.contentHash !== currentContentHash) {
			return { kind: "missing" };
		}
	}

	if (sentinel.status === "pass") return { kind: "pass" };
	return { kind: "fail", sentinel };
}

// ---------------------------------------------------------------------------
// Stop-event recursion guard
// ---------------------------------------------------------------------------

/**
 * Guard against infinite recursion on Stop events.
 *
 * When a Stop hook blocks, Claude Code re-fires the Stop event in a
 * "stop continuation" (with `stop_hook_active = true`). Without a guard,
 * blocking again creates an infinite loop.
 *
 * Behavior depends on `limit`:
 *   - `limit > 0` — Let `blockWithLimit` enforce the limit as usual.
 *   - `limit = 0` (unlimited) — Auto-allow immediately to prevent infinite loop.
 */
export function guardStopEvent(
	tag: string,
	adapter: HookAdapter,
	event: AgentEvent,
	limit: number,
): void {
	if (adapter.isStopRecursion(event)) {
		if (limit > 0) {
			log(tag, `stop_hook_active=true, limit=${limit} — deferring to blockWithLimit`);
			return;
		}
		log(tag, "stop_hook_active=true, limit=0 — auto-allowing (infinite-loop prevention)");
		adapter.allow();
	}
}

// ---------------------------------------------------------------------------
// Trigger pattern resolution
// ---------------------------------------------------------------------------

/** Common flags used by trigger resolution. */
type TriggerFlags = { on?: string; trigger?: string };

/**
 * Resolve trigger patterns from `--on` (named group) or `--trigger` (inline).
 * Returns an empty array when no trigger filter is set (matches everything).
 */
export function resolveTriggerPatterns(
	tag: string,
	config: ResolvedConfig,
	flags: TriggerFlags,
): string[] {
	if (flags.trigger) return [flags.trigger];
	if (flags.on) {
		const patterns = getTriggerPatterns(config, flags.on);
		if (!patterns) {
			log(tag, `Warning: trigger group "${flags.on}" not found in config`);
			return [];
		}
		return patterns;
	}
	return [];
}

// ---------------------------------------------------------------------------
// Trigger matching
// ---------------------------------------------------------------------------

/**
 * Check whether the current hook event matches any of the given trigger patterns.
 *
 * Trigger patterns are matched against the shell command string (case-insensitive
 * substring). Returns `true` if any pattern matches, or if `patterns` is empty.
 */
export function matchesTrigger(
	adapter: HookAdapter,
	event: AgentEvent,
	patterns: string[],
): boolean {
	if (patterns.length === 0) return true;

	const command = adapter.isShellToolCall(event) ? adapter.getShellCommand(event) : undefined;
	if (typeof command !== "string") return false;

	const lower = command.toLowerCase();
	return patterns.some((p) => lower.includes(p.toLowerCase()));
}

// ---------------------------------------------------------------------------
// Block-with-limit
// ---------------------------------------------------------------------------

/**
 * Block (or auto-allow if limit exceeded).
 *
 * Increments the consecutive failure counter and checks against the
 * configured limit. When `limit > 0` and the counter exceeds it,
 * auto-allows and resets the counter.
 */
export function blockWithLimit(
	tag: string,
	adapter: HookAdapter,
	config: ResolvedConfig,
	name: string,
	limit: number,
	reason: string,
): never {
	const count = incrementBlockCount(config.sentinelDir, config.projectDir, name);
	if (limit > 0 && count > limit) {
		log(
			tag,
			`WARNING: requirement not satisfied — block count ${count} exceeds limit ${limit}, auto-allowing`,
		);
		resetBlockCount(config.sentinelDir, config.projectDir, name);
		adapter.allow();
	}
	log(tag, `Action: block (${count}/${limit || "∞"}) — agent must re-run`);
	adapter.block(withProjectHeader(config.projectDir, reason));
}

/**
 * Block without incrementing the failure counter.
 * Used for intermediate / transient states (missing, pending).
 */
export function blockNoCount(
	tag: string,
	adapter: HookAdapter,
	reason: string,
	projectDir?: string,
): never {
	log(tag, "Action: block (no counter increment — transient state)");
	adapter.block(projectDir ? withProjectHeader(projectDir, reason) : reason);
}

// ---------------------------------------------------------------------------
// Project header
// ---------------------------------------------------------------------------

/**
 * Prepend a `[project: <dir>]` header to a block reason.
 */
function withProjectHeader(projectDir: string, reason: string): string {
	return `[project: ${projectDir}]\n${reason}`;
}
