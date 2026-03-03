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

export async function promptSelect<T>(
	message: string,
	items: T[],
	display: (item: T, index: number) => string,
): Promise<T> {
	if (process.stdin.isTTY) {
		return promptSelectInteractive(message, items, display);
	}
	return promptSelectFallback(message, items, display);
}

async function promptSelectInteractive<T>(
	message: string,
	items: T[],
	display: (item: T, index: number) => string,
): Promise<T> {
	const { render } = await import("ink");
	const { SelectPrompt } = await import("./select-prompt.tsx");
	const { createElement } = await import("react");

	const options = items.map((item, i) => ({
		label: display(item, i),
		value: String(i),
	}));

	let selectedIndex: number | undefined;

	const instance = render(
		createElement(SelectPrompt, {
			message,
			options,
			onSelect: (value: string) => {
				selectedIndex = Number.parseInt(value, 10);
			},
		}),
	);

	await instance.waitUntilExit();

	// Ink unrefs and pauses stdin on exit, which causes the process to
	// terminate before readline can set up. Restore stdin so subsequent
	// prompts work.
	process.stdin.resume();
	process.stdin.ref();

	if (selectedIndex === undefined) {
		process.exit(0);
	}

	// biome-ignore lint/style/noNonNullAssertion: index validated by Select options
	return items[selectedIndex]!;
}

async function promptSelectFallback<T>(
	message: string,
	items: T[],
	display: (item: T, index: number) => string,
): Promise<T> {
	const { cyan, yellow } = await import("./colors");

	console.log(message);
	for (let i = 0; i < items.length; i++) {
		// biome-ignore lint/style/noNonNullAssertion: bounds checked by loop
		console.log(`  ${cyan(String(i + 1))}  ${display(items[i]!, i)}`);
	}

	while (true) {
		const input = (await promptInput("Enter a number: ")).trim();
		const num = parseInt(input, 10);
		if (Number.isNaN(num) || num < 1 || num > items.length) {
			console.log(yellow(`  Please enter a number between 1 and ${items.length}.`));
			continue;
		}
		// biome-ignore lint/style/noNonNullAssertion: bounds validated above
		return items[num - 1]!;
	}
}
