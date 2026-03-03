/**
 * Task result handling.
 *
 * The task result uses the shape `{ "decision": "allow" | "block", "reason": "..." }`.
 * The `decision` field is the only hard requirement. On a "block" result the
 * full JSON is fed back to the agent so it can act on structured issues.
 */

import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import type { AgentEvent } from "./adapter";
import type { CommandName } from "./env";
import { expandPlaceholders } from "./placeholders";
import type { SentinelData } from "./sentinel";
import { sentinelPath } from "./sentinel";
import { readState } from "./state";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** The minimum required shape the agent writes as its task result. */
export type TaskResult = {
	decision: "allow" | "block";
	reason?: string;
	/** Opaque extra fields — preserved for pass-through to the agent. */
	[key: string]: unknown;
};

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

/**
 * Validate that a parsed object conforms to the `TaskResult` contract.
 * Only requires `decision` to be "allow" or "block".
 */
export function validateTaskResult(data: unknown): TaskResult | undefined {
	if (typeof data !== "object" || data === null) return undefined;
	const obj = data as Record<string, unknown>;
	if (obj.decision !== "allow" && obj.decision !== "block") return undefined;
	return data as TaskResult;
}

// ---------------------------------------------------------------------------
// Read task result (non-consuming)
// ---------------------------------------------------------------------------

/**
 * Read the task result file and translate to `SentinelData` without
 * deleting the file.
 */
export function readTaskResult(
	sentinelDir: string,
	projectDir: string,
	name: CommandName,
): SentinelData | undefined {
	const path = sentinelPath(sentinelDir, projectDir, name);
	if (!existsSync(path)) return undefined;

	let raw: string;
	try {
		raw = readFileSync(path, "utf-8");
	} catch {
		return undefined;
	}

	// Parse JSON
	let parsed: unknown;
	try {
		parsed = JSON.parse(raw);
	} catch {
		return undefined;
	}

	// Check if this is already internal SentinelData (legacy compat).
	const asRecord = parsed as Record<string, unknown>;
	if (typeof asRecord.status === "string" && typeof asRecord.startedAt === "string") {
		return parsed as SentinelData;
	}

	// Validate as TaskResult
	const result = validateTaskResult(parsed);
	if (!result) return undefined;

	// Translate TaskResult → SentinelData
	return taskResultToSentinel(result, raw);
}

// ---------------------------------------------------------------------------
// Translation
// ---------------------------------------------------------------------------

/**
 * Convert a validated `TaskResult` into `SentinelData`.
 */
export function taskResultToSentinel(result: TaskResult, rawJson?: string): SentinelData {
	const now = new Date().toISOString();

	if (result.decision === "allow") {
		return {
			status: "pass",
			startedAt: now,
			finishedAt: now,
			details: result.reason ?? "Task passed.",
		};
	}

	return {
		status: "fail",
		startedAt: now,
		finishedAt: now,
		details: result.reason ?? "(no reason provided)",
		rawResult: rawJson,
	};
}

// ---------------------------------------------------------------------------
// Instructions loading (shared by task.ts and sync.ts)
// ---------------------------------------------------------------------------

/**
 * Resolve, read, and expand placeholders in a task instructions file.
 *
 * Returns `undefined` if the path is absent, the file doesn't exist,
 * or any error occurs — instructions are always treated as optional.
 */
export async function loadInstructions(
	instructionsRaw: string | undefined,
	projectDir: string,
	sentinelDir: string,
	staged: boolean | undefined,
	event: AgentEvent,
): Promise<string | undefined> {
	if (!instructionsRaw) return undefined;

	const resolved = instructionsRaw.startsWith("/")
		? instructionsRaw
		: join(projectDir, instructionsRaw);

	try {
		if (!existsSync(resolved)) return undefined;
		let instructions = readFileSync(resolved, "utf-8");
		const state = readState(sentinelDir, projectDir);
		instructions = await expandPlaceholders(instructions, {
			state,
			projectDir,
			staged,
			event,
		});
		return instructions;
	} catch {
		return undefined;
	}
}

// ---------------------------------------------------------------------------
// Schema resolution (shared by task.ts and sync.ts)
// ---------------------------------------------------------------------------

/**
 * Resolve the task result schema content.
 *
 * If `schemaRaw` is a path (absolute or project-relative) pointing to an
 * existing file, its content is returned. Otherwise `DEFAULT_TASK_SCHEMA`
 * is used as a fallback.
 */
export function resolveTaskSchemaContent(
	projectDir: string,
	schemaRaw: string | undefined,
): string {
	if (!schemaRaw) return DEFAULT_TASK_SCHEMA;
	const schemaPath = schemaRaw.startsWith("/") ? schemaRaw : join(projectDir, schemaRaw);
	if (existsSync(schemaPath)) {
		return readFileSync(schemaPath, "utf-8").trim();
	}
	return DEFAULT_TASK_SCHEMA;
}

// ---------------------------------------------------------------------------
// Default schema (for inclusion in block messages)
// ---------------------------------------------------------------------------

/** Default task result JSON Schema presented to the agent in block messages. */
export const DEFAULT_TASK_SCHEMA = `{
  "type": "object",
  "required": ["decision"],
  "properties": {
    "decision": {
      "enum": ["allow", "block"],
      "description": "allow if no issues found, block if issues require fixing"
    },
    "reason": {
      "type": "string",
      "description": "Short summary of the task outcome"
    },
    "issues": {
      "type": "array",
      "maxItems": 5,
      "items": {
        "type": "object",
        "required": ["severity", "message"],
        "properties": {
          "severity": { "enum": ["CRITICAL", "HIGH"] },
          "file": { "type": "string", "description": "path:line" },
          "message": { "type": "string", "description": "What is wrong (1-2 sentences)" }
        }
      }
    }
  }
}`;
