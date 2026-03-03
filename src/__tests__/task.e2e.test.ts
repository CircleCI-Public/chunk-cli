import { describe, it } from "bun:test";

describe("chunk task run", () => {
	it.todo("exits 2 when --definition flag is missing", () => {});
	it.todo("exits 2 when --prompt flag is missing", () => {});
	it.todo("exits 2 when CIRCLECI_TOKEN is not set", () => {});
	it.todo("exits 2 when .chunk/run.json does not exist", () => {});
	it.todo("exits 0 and prints run ID when CircleCI API call succeeds", () => {});
});

describe("chunk task config", () => {
	it.todo("exits 2 when not run inside a git repository", () => {});
	it.todo("exits 0 and writes .chunk/run.json when wizard is completed", () => {});
});
