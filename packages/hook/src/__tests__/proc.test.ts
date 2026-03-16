import { describe, expect, it } from "bun:test";

import { runCommand } from "../lib/proc";

describe("runCommand", () => {
	describe("extendEnv", () => {
		it("inherits process.env by default", async () => {
			// Use a variable we inject ourselves — $HOME is unset in some minimal
			// CI containers and would produce empty output, causing a false failure.
			process.env.PROC_TEST_INHERIT = "inherited";
			try {
				const result = await runCommand({ command: "echo $PROC_TEST_INHERIT", timeout: 10 });
				expect(result.exitCode).toBe(0);
				expect(result.output).toBe("inherited");
			} finally {
				delete process.env.PROC_TEST_INHERIT;
			}
		});

		it("inherits process.env when extendEnv is true", async () => {
			process.env.PROC_TEST_EXPLICIT = "explicit";
			try {
				const result = await runCommand({
					command: "echo $PROC_TEST_EXPLICIT",
					timeout: 10,
					extendEnv: true,
				});
				expect(result.exitCode).toBe(0);
				expect(result.output).toBe("explicit");
			} finally {
				delete process.env.PROC_TEST_EXPLICIT;
			}
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
