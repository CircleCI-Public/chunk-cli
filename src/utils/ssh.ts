import * as crypto from "node:crypto";
import * as fs from "node:fs";
import * as path from "node:path";
import * as tls from "node:tls";
import { Client } from "ssh2";

export interface SshResult {
	exitCode: number;
	stdout: string;
	stderr: string;
}

export function shellEscape(arg: string): string {
	return `'${arg.replace(/'/g, "'\\''")}'`;
}

/**
 * Join args into a shell command string using POSIX single-quote escaping.
 * Safe to pass to client.exec() — the SSH exec channel runs the command
 * through the remote login shell (/bin/sh -c <cmd>), so quoting is required.
 */
export function shellJoin(args: string[]): string {
	return args.map(shellEscape).join(" ");
}

const SANDBOX_SSH_PORT = 2222; // TLS terminator port on CircleCI sandbox hosts
const SANDBOX_SSH_USER = "user";

export function tofuVerifyHostKey(
	knownHostsFile: string,
	host: string,
	fingerprint: string,
): boolean {
	let contents = "";
	try {
		contents = fs.readFileSync(knownHostsFile, "utf8");
	} catch (err) {
		const e = err as NodeJS.ErrnoException;
		if (e.code !== "ENOENT") throw err;
		fs.mkdirSync(path.dirname(knownHostsFile), { recursive: true });
	}

	for (const line of contents.split("\n")) {
		const trimmed = line.trim();
		if (!trimmed || trimmed.startsWith("#")) continue;
		const [storedHost, storedFingerprint] = trimmed.split(/\s+/);
		if (storedHost === host) {
			return storedFingerprint === fingerprint;
		}
	}

	fs.appendFileSync(knownHostsFile, `${host} ${fingerprint}\n`, "utf8");
	return true;
}

export async function execOverSsh(
	sandboxUrl: string,
	identityFile: string,
	knownHostsFile: string,
	command: string[],
	options?: { stdin?: string | Buffer },
): Promise<SshResult> {
	return new Promise((resolve, reject) => {
		const client = new Client();

		// rejectUnauthorized: false — the sandbox TLS terminator uses a self-signed
		// certificate. We deliberately skip cert validation here and rely on SSH
		// host key pinning (tofuVerifyHostKey) as the trust anchor instead.
		const tlsSocket = tls.connect({
			host: sandboxUrl,
			port: SANDBOX_SSH_PORT,
			rejectUnauthorized: false,
		});

		tlsSocket.once("error", reject);

		tlsSocket.once("secureConnect", () => {
			client.connect({
				sock: tlsSocket,
				username: SANDBOX_SSH_USER,
				privateKey: fs.readFileSync(identityFile),
				hostVerifier: (key: Buffer) => {
					const fingerprint = crypto.createHash("sha256").update(key).digest("hex");
					return tofuVerifyHostKey(knownHostsFile, sandboxUrl, fingerprint);
				},
			});
		});

		client.once("ready", () => {
			client.exec(shellJoin(command), (err, stream) => {
				if (err) {
					client.end();
					return reject(err);
				}

				const stdoutChunks: Buffer[] = [];
				const stderrChunks: Buffer[] = [];

				stream.on("data", (chunk: Buffer) => stdoutChunks.push(chunk));
				stream.stderr.on("data", (chunk: Buffer) => stderrChunks.push(chunk));

				stream.once("close", (exitCode: number | null) => {
					client.end();
					resolve({
						exitCode: exitCode ?? 1,
						stdout: Buffer.concat(stdoutChunks).toString("utf8"),
						stderr: Buffer.concat(stderrChunks).toString("utf8"),
					});
				});

				stream.stdin.end(options?.stdin);
			});
		});

		client.once("error", (err) => {
			tlsSocket.destroy();
			reject(err);
		});
	});
}
