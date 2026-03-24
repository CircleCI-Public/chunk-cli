/**
 * Rewrite `validate:<name>` colon syntax to `validate <name>` so
 * Commander sees a standard command with a positional argument.
 */
export function rewriteColonSyntax(argv: string[]): string[] {
	const args = argv.slice();
	for (let i = 2; i < args.length; i++) {
		const match = args[i]?.match(/^validate:(.+)$/);
		if (match?.[1]) {
			args.splice(i, 1, "validate", match[1]);
			break;
		}
	}
	return args;
}
