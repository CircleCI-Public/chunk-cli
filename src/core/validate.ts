import { spawn } from "node:child_process";
import { CircleCIError } from "../api/circleci";
import { loadProjectConfig } from "../storage/project-config";
import { shellEscape, withSshConnection } from "../utils/ssh";
import { openSandboxSession } from "./sandbox-session";

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
	| { type: "remote"; orgId: string; sandboxId: string; token: string; identityFile?: string; dest?: string };

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

export function loadCommands():
	| { commands: string[] }
	| { ok: false; error: string; hint?: string } {
	const config = loadProjectConfig();
	const commands: string[] = [];
	if (config.installCommand) commands.push(config.installCommand);
	if (config.testCommand) commands.push(config.testCommand);

	if (commands.length === 0) {
		return {
			ok: false,
			error: "No validate commands configured",
			hint: "Run `chunk validate init` to detect your install and test commands.",
		};
	}
	return { commands };
}

const noop = () => {};

export async function validateLocally(
	_projectDir: string,
	onCommandStart: (command: string) => void = noop,
	onCommandOutput: (stdout: string | null, stderr: string | null) => void = noop,
): Promise<ValidateResult> {
	const loaded = loadCommands();
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
	_projectDir: string,
	identityFile?: string,
	dest?: string,
	onCommandStart: (command: string) => void = noop,
	onCommandOutput: (stdout: string | null, stderr: string | null) => void = noop,
): Promise<ValidateResult> {
	const loaded = loadCommands();
	if ("ok" in loaded) return loaded;

	let session: Awaited<ReturnType<typeof openSandboxSession>>;
	try {
		session = await openSandboxSession(sandboxId, organizationId, circleciToken, identityFile);
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

	try {
		return await withSshConnection(
			session.sandboxUrl,
			session.identityFile,
			session.knownHostsFile,
			async (exec) => {
				const executor: CommandExecutor = async (command, onOutput) => {
					const cmd = dest ? `cd ${shellEscape(dest)} && ${command}` : command;
					const result = await exec(["sh", "-c", cmd]);
					onOutput(result.stdout || null, result.stderr || null);
					return result;
				};
				return runValidateSequence(
					loaded.commands,
					onCommandStart,
					onCommandOutput,
					executor,
				);
			},
		);
	} catch (error) {
		return {
			ok: false,
			error: error instanceof Error ? error.message : String(error),
			hint: "Check that the sandbox is running and your SSH key is registered.",
		};
	}
}

export async function runValidate(
	projectDir: string,
	mode: ValidateMode,
	onCommandStart: (command: string) => void = noop,
	onCommandOutput: (stdout: string | null, stderr: string | null) => void = noop,
): Promise<ValidateCommandResult> {
	if (mode.type === "dry-run") {
		const loaded = loadCommands();
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
		mode.identityFile,
		mode.dest,
		onCommandStart,
		onCommandOutput,
	);
}
