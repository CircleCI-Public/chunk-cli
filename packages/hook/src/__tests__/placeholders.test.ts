import { describe, expect, it } from "bun:test";
import { expandPlaceholders } from "../lib/placeholders";
import type { State } from "../lib/state";

// Stub git functions passed via opts.git to avoid real git operations and
// module-level mocking that would pollute the shared module registry.
const stubGit = {
	getChangedFiles: async () => ["src/lib/state.ts", "src/commands/task.ts"],
	getChangedPackages: (files: string[]) => {
		const dirs = new Set<string>();
		for (const file of files) {
			const parts = file.split("/");
			parts.pop();
			dirs.add(parts.length === 0 ? "./" : `./${parts.join("/")}`);
		}
		return [...dirs].sort();
	},
};

describe("placeholders", () => {
	describe("expandPlaceholders()", () => {
		const baseOpts = {
			state: {} as State,
			projectDir: "/test/project",
			git: stubGit,
		};

		it("returns template unchanged when no placeholders", async () => {
			const result = await expandPlaceholders("No placeholders here", baseOpts);
			expect(result).toBe("No placeholders here");
		});

		it("resolves event-namespaced state fields via dot notation (__entries sugar)", async () => {
			const result = await expandPlaceholders("Task: {{UserPromptSubmit.prompt}}", {
				...baseOpts,
				state: {
					UserPromptSubmit: { __entries: [{ prompt: "Fix the login bug" }] },
				},
			});
			expect(result).toBe("Task: Fix the login bug");
		});

		it("resolves fields via explicit bracket notation", async () => {
			const result = await expandPlaceholders(
				"First: {{UserPromptSubmit[0].prompt}} Second: {{UserPromptSubmit[1].prompt}}",
				{
					...baseOpts,
					state: {
						UserPromptSubmit: {
							__entries: [{ prompt: "first" }, { prompt: "second" }],
						},
					},
				},
			);
			expect(result).toBe("First: first Second: second");
		});

		it("resolves fields from multiple events", async () => {
			const result = await expandPlaceholders(
				"{{UserPromptSubmit.prompt}} (active: {{Stop.stop_hook_active}})",
				{
					...baseOpts,
					state: {
						UserPromptSubmit: { __entries: [{ prompt: "review code" }] },
						Stop: { __entries: [{ stop_hook_active: true }] },
					},
				},
			);
			expect(result).toBe("review code (active: true)");
		});

		it("resolves deeply nested state fields", async () => {
			const result = await expandPlaceholders("Cmd: {{PreToolUse.tool_input.command}}", {
				...baseOpts,
				state: {
					PreToolUse: {
						__entries: [{ tool_input: { command: "git commit" } }],
					},
				},
			});
			expect(result).toBe("Cmd: git commit");
		});

		it("resolves {{CHANGED_FILES}} from git", async () => {
			const result = await expandPlaceholders("Files: {{CHANGED_FILES}}", baseOpts);
			expect(result).toBe("Files: src/lib/state.ts src/commands/task.ts");
		});

		it("resolves {{CHANGED_PACKAGES}} from git", async () => {
			const result = await expandPlaceholders("Packages: {{CHANGED_PACKAGES}}", baseOpts);
			expect(result).toBe("Packages: ./src/commands ./src/lib");
		});

		it("replaces unresolved placeholders with empty string", async () => {
			const result = await expandPlaceholders("{{UserPromptSubmit.prompt}} and {{Missing.field}}", {
				...baseOpts,
				state: { UserPromptSubmit: { __entries: [{ prompt: "resolved" }] } },
			});
			expect(result).toBe("resolved and ");
		});

		it("handles template with only unresolved placeholders", async () => {
			const result = await expandPlaceholders("{{Missing.a}} {{Missing.b}}", baseOpts);
			expect(result).toBe(" ");
		});

		it("handles empty template", async () => {
			const result = await expandPlaceholders("", baseOpts);
			expect(result).toBe("");
		});

		it("handles complex template with mixed placeholders", async () => {
			const result = await expandPlaceholders(
				"Review {{CHANGED_FILES}} for {{UserPromptSubmit.prompt}} issues",
				{
					...baseOpts,
					state: { UserPromptSubmit: { __entries: [{ prompt: "security" }] } },
				},
			);
			expect(result).toBe("Review src/lib/state.ts src/commands/task.ts for security issues");
		});
	});

	describe("event overlay", () => {
		const baseOpts = {
			state: {} as State,
			projectDir: "/test/project",
		};

		it("resolves triggering event fields from event", async () => {
			const result = await expandPlaceholders("Transcript: {{Stop.transcript_path}}", {
				...baseOpts,
				event: {
					eventName: "Stop",
					raw: {
						hook_event_name: "Stop",
						transcript_path: "/home/user/.claude/projects/abc.jsonl",
						session_id: "sess-123",
						stop_hook_active: false,
					},
				},
			});
			expect(result).toBe("Transcript: /home/user/.claude/projects/abc.jsonl");
		});

		it("resolves multiple fields from the same event", async () => {
			const result = await expandPlaceholders(
				"{{Stop.session_id}} active={{Stop.stop_hook_active}}",
				{
					...baseOpts,
					event: {
						eventName: "Stop",
						raw: {
							hook_event_name: "Stop",
							session_id: "sess-456",
							stop_hook_active: true,
						},
					},
				},
			);
			expect(result).toBe("sess-456 active=true");
		});

		it("event overlays without overwriting other events in state", async () => {
			const result = await expandPlaceholders(
				"{{UserPromptSubmit.prompt}} transcript={{Stop.transcript_path}}",
				{
					...baseOpts,
					state: { UserPromptSubmit: { __entries: [{ prompt: "fix bug" }] } },
					event: {
						eventName: "Stop",
						raw: {
							hook_event_name: "Stop",
							transcript_path: "/path/to/transcript.jsonl",
						},
					},
				},
			);
			expect(result).toBe("fix bug transcript=/path/to/transcript.jsonl");
		});

		it("event merges with existing state for the same event", async () => {
			const result = await expandPlaceholders(
				"saved={{Stop.custom_field}} live={{Stop.transcript_path}}",
				{
					...baseOpts,
					state: { Stop: { __entries: [{ custom_field: "from-save" }] } },
					event: {
						eventName: "Stop",
						raw: {
							hook_event_name: "Stop",
							transcript_path: "/path/to/transcript.jsonl",
						},
					},
				},
			);
			expect(result).toBe("saved=from-save live=/path/to/transcript.jsonl");
		});

		it("event overrides same-named fields from saved state", async () => {
			const result = await expandPlaceholders("{{Stop.session_id}}", {
				...baseOpts,
				state: { Stop: { __entries: [{ session_id: "old-saved" }] } },
				event: {
					eventName: "Stop",
					raw: {
						hook_event_name: "Stop",
						session_id: "current-live",
					},
				},
			});
			expect(result).toBe("current-live");
		});

		it("normalizes camelCase event names (Cursor compatibility)", async () => {
			const result = await expandPlaceholders("Transcript: {{Stop.transcript_path}}", {
				...baseOpts,
				event: {
					eventName: "stop",
					raw: {
						hook_event_name: "stop",
						transcript_path: "/home/user/.cursor/transcript.jsonl",
						session_id: "cursor-sess",
					},
				},
			});
			expect(result).toBe("Transcript: /home/user/.cursor/transcript.jsonl");
		});

		it("normalizes Cursor beforeSubmitPrompt → UserPromptSubmit", async () => {
			const result = await expandPlaceholders("Prompt: {{UserPromptSubmit.prompt}}", {
				...baseOpts,
				event: {
					eventName: "beforeSubmitPrompt",
					raw: {
						hook_event_name: "beforeSubmitPrompt",
						prompt: "fix the login bug",
					},
				},
			});
			expect(result).toBe("Prompt: fix the login bug");
		});

		it("no-ops when event has no eventName", async () => {
			const result = await expandPlaceholders("{{Stop.transcript_path}}", {
				...baseOpts,
				event: {
					eventName: "",
					raw: {
						transcript_path: "/path/to/file.jsonl",
					},
				},
			});
			// Cannot namespace without event name, so placeholder resolves to empty
			expect(result).toBe("");
		});

		it("no-ops when event is undefined", async () => {
			const result = await expandPlaceholders("{{Stop.transcript_path}}", {
				...baseOpts,
				// event omitted
			});
			expect(result).toBe("");
		});
	});
});
