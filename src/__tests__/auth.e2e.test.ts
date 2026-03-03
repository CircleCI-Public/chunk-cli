import { describe, it } from "bun:test";

describe("chunk auth login", () => {
	it.todo("exits 2 when invoked non-interactively without input", () => {});
	it.todo("saves API key and exits 0 when a valid sk-ant- key is entered", () => {});
	it.todo("exits 2 when entered key is blank", () => {});
	it.todo("exits 2 when entered key lacks sk-ant- prefix", () => {});
});

describe("chunk auth status", () => {
	it.todo("exits 0 and reports unauthenticated when no API key is stored", () => {});
	it.todo("exits 0 and reports authenticated when a valid key is in config", () => {});
	it.todo("exits 0 and reports key source as env when ANTHROPIC_API_KEY is set", () => {});
	it.todo("exits 1 when stored key fails Anthropic validation", () => {});
});

describe("chunk auth logout", () => {
	it.todo("exits 0 and removes stored credentials when confirmed", () => {});
	it.todo("exits 0 and reports nothing to remove when not logged in", () => {});
});
