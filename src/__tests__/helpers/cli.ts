import { resolve } from "node:path";

const ROOT = resolve(import.meta.dir, "../../..");

export async function runCLI(
	args: string[],
	opts: { env?: Record<string, string>; cwd?: string } = {},
) {
	const proc = Bun.spawn(["bun", "run", resolve(ROOT, "src/index.ts"), ...args], {
		stdout: "pipe",
		stderr: "pipe",
		env: { ...process.env, ...opts.env },
		cwd: opts.cwd ?? ROOT,
	});
	const [stdout, stderr, exitCode] = await Promise.all([
		new Response(proc.stdout).text(),
		new Response(proc.stderr).text(),
		proc.exited,
	]);
	return { stdout, stderr, exitCode };
}
