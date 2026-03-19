import type { Command } from "@commander-js/extra-typings";
import omelette from "omelette";

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

function createCompletion(program: Command) {
	const tree = buildTree(program);
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
