import { describe, expect, it } from "bun:test";

import { runCommand } from "../lib/proc";

describe("runCommand", () => {
	describe("extendEnv", () => {
		it("inherits process.env by default", async () => {
			const result = await runCommand({ command: "echo $HOME", timeout: 10 });
			expect(result.exitCode).toBe(0);
			expect(result.output).not.toBe("");
		});

		it("inherits process.env when extendEnv is true", async () => {
			const result = await runCommand({
				command: "echo $HOME",
				timeout: 10,
				extendEnv: true,
			});
			expect(result.exitCode).toBe(0);
			expect(result.output).not.toBe("");
		});

		it("does NOT inherit process.env when extendEnv is false", async () => {
			const result = await runCommand({
				command: 'echo "HOME=$HOME"',
				timeout: 10,
				env: { PATH: process.env.PATH ?? "/usr/bin:/bin" },
				extendEnv: false,
			});
			expect(result.exitCode).toBe(0);
			// HOME should be empty since process.env is not inherited
			expect(result.output).toBe("HOME=");
		});

		it("uses provided env as complete environment when extendEnv is false", async () => {
			const result = await runCommand({
				command: "echo $CUSTOM_VAR",
				timeout: 10,
				env: {
					PATH: process.env.PATH ?? "/usr/bin:/bin",
					CUSTOM_VAR: "hello_clean",
				},
				extendEnv: false,
			});
			expect(result.exitCode).toBe(0);
			expect(result.output).toBe("hello_clean");
		});

		it("merges env on top of process.env when extendEnv is true", async () => {
			const result = await runCommand({
				command: "echo $MY_TEST_VAR",
				timeout: 10,
				env: { MY_TEST_VAR: "from_extra" },
				extendEnv: true,
			});
			expect(result.exitCode).toBe(0);
			expect(result.output).toBe("from_extra");
		});
	});
});
