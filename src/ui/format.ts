/**
 * Terminal output formatting primitives
 *
 * format* functions return strings (no side effects).
 * print* functions write to stdout/stderr with a leading newline.
 * Built on colors.ts.
 */

import { bold, dim, green, yellow } from "./colors";

export function formatSuccess(message: string): string {
	return green(`✓ ${message}`);
}

export function formatWarning(message: string): string {
	return yellow(`⚠ ${message}`);
}

/** Pad and style a label for aligned key-value output. */
export function label(text: string, width: number, style: (s: string) => string = bold): string {
	return style(text.padEnd(width));
}

/** Format a numbered pipeline step header. */
export function formatStep(current: number, total: number, title: string): string {
	return `${dim(`Step ${current}/${total}`)}  ${bold(title)}`;
}

export function printSuccess(message: string, stream: "stdout" | "stderr" = "stdout"): void {
	const write = stream === "stderr" ? console.error : console.log;
	write(`\n${formatSuccess(message)}`);
}

export function printWarning(message: string, stream: "stdout" | "stderr" = "stdout"): void {
	const write = stream === "stderr" ? console.error : console.log;
	write(`\n${formatWarning(message)}`);
}
