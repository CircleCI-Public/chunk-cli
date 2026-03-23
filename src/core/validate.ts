import { spawn } from "node:child_process";
import { CircleCIError, createSandboxAccessToken, execCommand } from "../api/circleci";
import { loadSequenceCommands } from "./run-config";

export type ValidateStepResult = {
	command: string;
	exitCode: number;
	stdout: string;
	stderr: string;
};

export type ValidateResult =
	| { ok: true; results: ValidateStepResult[]; skipped: string[] }
	| { ok: false; error: string; hint?: string };

export type ValidateMode =
	| { type: "dry-run" }
	| { type: "local" }
	| { type: "remote"; orgId: string; sandboxId: string; token: string };

export type ValidateCommandResult =
	| { ok: true; results: ValidateStepResult[]; skipped: string[] }
	| { ok: true; dryRun: true; commands: string[] }
	| { ok: false; error: string; hint?: string };

type CommandExecutor = (
	command: string,
	onOutput: (stdout: string | null, stderr: string | null) => void,
) => Promise<{ exitCode: number; stdout: string; stderr: string }>;

async function runValidateSequence(
	commands: string[],
	onCommandStart: (command: string) => void,
	onCommandOutput: (stdout: string | null, stderr: string | null) => void,
	executor: CommandExecutor,
): Promise<{ ok: true; results: ValidateStepResult[]; skipped: string[] }> {
	const results: ValidateStepResult[] = [];

	for (const command of commands) {
		onCommandStart(command);
		const { exitCode, stdout, stderr } = await executor(command, onCommandOutput);
		results.push({ command, exitCode, stdout, stderr });
		if (exitCode !== 0) break;
	}

	const skipped = commands.slice(results.length);
	return { ok: true, results, skipped };
}

export function loadCommands(
	projectDir: string,
): { commands: string[] } | { ok: false; error: string; hint?: string } {
	return loadSequenceCommands(projectDir);
}

const noop = () => {};

export async function validateLocally(
	projectDir: string,
	onCommandStart: (command: string) => void = noop,
	onCommandOutput: (stdout: string | null, stderr: string | null) => void = noop,
): Promise<ValidateResult> {
	const loaded = loadCommands(projectDir);
	if ("ok" in loaded) return loaded;

	const executor: CommandExecutor = (command, onOutput) =>
		new Promise<{ exitCode: number; stdout: string; stderr: string }>((resolve, reject) => {
			const proc = spawn("sh", ["-c", command], {
				stdio: ["ignore", "pipe", "pipe"],
			});
			const stdoutChunks: Buffer[] = [];
			const stderrChunks: Buffer[] = [];
			proc.stdout.on("data", (chunk: Buffer) => {
				stdoutChunks.push(chunk);
				onOutput(chunk.toString(), null);
			});
			proc.stderr.on("data", (chunk: Buffer) => {
				stderrChunks.push(chunk);
				onOutput(null, chunk.toString());
			});
			// ChildProcessByStdio lacks EventEmitter methods in some @types/node versions
			const emitter = proc as unknown as NodeJS.EventEmitter;
			emitter.on("close", (code: number | null) =>
				resolve({
					exitCode: code ?? 1,
					stdout: Buffer.concat(stdoutChunks).toString(),
					stderr: Buffer.concat(stderrChunks).toString(),
				}),
			);
			emitter.on("error", reject);
		});
	return runValidateSequence(loaded.commands, onCommandStart, onCommandOutput, executor);
}

export async function validateOnSandbox(
	organizationId: string,
	sandboxId: string,
	circleciToken: string,
	projectDir: string,
	onCommandStart: (command: string) => void = noop,
	onCommandOutput: (stdout: string | null, stderr: string | null) => void = noop,
): Promise<ValidateResult> {
	const loaded = loadCommands(projectDir);
	if ("ok" in loaded) return loaded;

	let accessToken: string;
	try {
		const tokenResp = await createSandboxAccessToken(sandboxId, organizationId, circleciToken);
		accessToken = tokenResp.access_token;
	} catch (error) {
		if (error instanceof CircleCIError) {
			return {
				ok: false,
				error: error.message,
				hint: "Check your CIRCLE_TOKEN, sandbox ID, and org ID.",
			};
		}
		throw error;
	}

	const executor: CommandExecutor = async (command, onOutput) => {
		const result = await execCommand("sh", ["-c", command], accessToken);
		const stdout = result.stdout ?? "";
		const stderr = result.stderr ?? "";
		onOutput(stdout || null, stderr || null);
		return { exitCode: result.exit_code, stdout, stderr };
	};

	try {
		return await runValidateSequence(loaded.commands, onCommandStart, onCommandOutput, executor);
	} catch (error) {
		if (error instanceof CircleCIError) return { ok: false, error: error.message };
		throw error;
	}
}

export async function runValidate(
	projectDir: string,
	mode: ValidateMode,
	onCommandStart: (command: string) => void = noop,
	onCommandOutput: (stdout: string | null, stderr: string | null) => void = noop,
): Promise<ValidateCommandResult> {
	if (mode.type === "dry-run") {
		const loaded = loadCommands(projectDir);
		if ("ok" in loaded) return loaded;
		return { ok: true, dryRun: true, commands: loaded.commands };
	}
	if (mode.type === "local") {
		return validateLocally(projectDir, onCommandStart, onCommandOutput);
	}
	return validateOnSandbox(
		mode.orgId,
		mode.sandboxId,
		mode.token,
		projectDir,
		onCommandStart,
		onCommandOutput,
	);
}
