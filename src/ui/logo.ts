/**
 * ASCII art logo branding
 */

export const LOGO = `        █████████████████
      █████████████████████
    ███████████████████  ███
  ███                ███████
███                   ██████
███                   ██████
███       ██ ██       ██████
███   ██         ██   ██████
███     █████████     ██████
███                   ██████
███                   ██████
███                   ████
  ███                ██
    █████████████████`;

/** Print the logo with text lines placed to its right, vertically centered. */
export function printBanner(lines: string[], stream: "stdout" | "stderr" = "stdout"): void {
	const write = stream === "stderr" ? console.error : console.log;
	const logoLines = LOGO.split("\n");
	const logoWidth = Math.max(...logoLines.map((l) => l.length));
	const gap = "   ";
	const startLine = Math.floor((logoLines.length - lines.length) / 2);

	const banner = logoLines
		.map((logoLine, i) => {
			const textIndex = i - startLine;
			if (textIndex >= 0 && textIndex < lines.length) {
				return `${logoLine.padEnd(logoWidth)}${gap}${lines[textIndex]}`;
			}
			return logoLine;
		})
		.join("\n");

	write(`\n${banner}\n`);
}
