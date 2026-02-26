import * as readline from "node:readline";

export async function promptInput(
	message: string,
	options: { hidden?: boolean } = {},
): Promise<string> {
	const rl = readline.createInterface({
		input: process.stdin,
		output: process.stdout,
	});

	return new Promise((resolve) => {
		if (options.hidden && process.stdin.isTTY) {
			process.stdout.write(message);
			let input = "";

			const stdin = process.stdin;
			stdin.setRawMode(true);
			stdin.resume();
			stdin.setEncoding("utf8");

			const onData = (char: string) => {
				const charCode = char.charCodeAt(0);

				if (char === "\r" || char === "\n") {
					stdin.setRawMode(false);
					stdin.removeListener("data", onData);
					rl.close();
					process.stdout.write("\n");
					resolve(input);
				} else if (charCode === 3) {
					// Ctrl+C
					stdin.setRawMode(false);
					stdin.removeListener("data", onData);
					rl.close();
					process.exit(0);
				} else if (charCode === 127 || charCode === 8) {
					// Backspace
					if (input.length > 0) {
						input = input.slice(0, -1);
					}
				} else if (charCode >= 32) {
					input += char;
				}
			};

			stdin.on("data", onData);
		} else {
			rl.question(message, (answer) => {
				rl.close();
				resolve(answer);
			});
		}
	});
}

export async function promptConfirm(message: string): Promise<boolean> {
	const answer = await promptInput(`${message} (y/n): `);
	return answer.toLowerCase() === "y" || answer.toLowerCase() === "yes";
}
