import { describe, it } from "bun:test";

describe("chunk hook --help", () => {
	it.todo("exits 0 and lists exec, task, sync, state, scope, repo, env subcommands", () => {});
});

describe("chunk hook exec", () => {
	it.todo("exits 0 when exec check passes", () => {});
	it.todo("exits 2 when exec check fails", () => {});
});

describe("chunk hook task", () => {
	it.todo("exits 0 when task sentinel is passing", () => {});
	it.todo("exits 2 when task sentinel is missing or failing", () => {});
});

describe("chunk hook sync", () => {
	it.todo("exits 0 when all specs pass", () => {});
	it.todo("exits 2 when one or more specs fail", () => {});
});

describe("chunk hook state", () => {
	it.todo("saves and loads hook state round-trip", () => {});
	it.todo("clears hook state", () => {});
});

describe("chunk hook repo init", () => {
	it.todo("exits 0 and copies template files into .chunk/hook/", () => {});
});
