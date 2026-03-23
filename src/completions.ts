import type { Command } from "@commander-js/extra-typings";
import omelette from "omelette";
import { listCommands } from "./core/run-config";

type CompletionTree = { [key: string]: CompletionTree | [] };

export function buildTree(cmd: Command): CompletionTree | [] {
	if (cmd.commands.length === 0) return [];
	const tree: CompletionTree = {};
	for (const sub of cmd.commands) {
		if (sub.name() === "help") continue;
		tree[sub.name()] = buildTree(sub as Command);
	}
	return tree;
}

/**
 * Inject `validate:<name>` entries from `.chunk/commands.json`
 * so tab completion works with the colon syntax.
 */
function injectValidateCompletions(tree: CompletionTree): void {
	try {
		const commands = listCommands(process.cwd());
		for (const cmd of commands) {
			tree[`validate:${cmd.name}`] = [];
		}
		// Always include validate:init
		tree["validate:init"] = [];
	} catch {
		// Config may not exist — completions are best-effort
	}
}

function createCompletion(program: Command) {
	const tree = buildTree(program);
	if (typeof tree === "object" && !Array.isArray(tree)) {
		injectValidateCompletions(tree);
	}
	return omelette("chunk").tree(tree as CompletionTree);
}

export function initCompletions(program: Command): void {
	createCompletion(program).init();
}

export function setupShellCompletion(program: Command): void {
	createCompletion(program).setupShellInitFile();
}

export function teardownShellCompletion(program: Command): void {
	createCompletion(program).cleanupShellInitFile();
}
