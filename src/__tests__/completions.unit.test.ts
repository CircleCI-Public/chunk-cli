import { describe, expect, it } from "bun:test";
import { Command } from "@commander-js/extra-typings";
import { buildTree } from "../completions";

describe("buildTree", () => {
	it("returns [] for a leaf command with no subcommands", () => {
		const cmd = new Command("leaf");
		expect(buildTree(cmd)).toEqual([]);
	});

	it("returns a flat tree for a command with leaf subcommands", () => {
		const cmd = new Command("root");
		cmd.addCommand(new Command("foo"));
		cmd.addCommand(new Command("bar"));
		expect(buildTree(cmd)).toEqual({ foo: [], bar: [] });
	});

	it("returns a nested tree for multi-level subcommands", () => {
		const cmd = new Command("root");
		const sub = new Command("auth");
		sub.addCommand(new Command("login"));
		sub.addCommand(new Command("logout"));
		cmd.addCommand(sub);
		expect(buildTree(cmd)).toEqual({ auth: { login: [], logout: [] } });
	});

	it("filters out the auto-added help subcommand", () => {
		const cmd = new Command("root");
		cmd.addCommand(new Command("foo"));
		// Commander adds a 'help' subcommand automatically when subcommands exist
		const tree = buildTree(cmd);
		expect(tree).not.toHaveProperty("help");
		expect(tree).toEqual({ foo: [] });
	});
});
