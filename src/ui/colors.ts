/**
 * ANSI color utilities for terminal output
 */

let colorEnabled = true;

export function setColorEnabled(enabled: boolean): void {
	colorEnabled = enabled;
}

export function isColorEnabled(): boolean {
	return colorEnabled;
}

function wrap(code: string, text: string): string {
	if (!colorEnabled) return text;
	return `\x1b[${code}m${text}\x1b[0m`;
}

// Basic colors
export const red = (text: string): string => wrap("31", text);
export const green = (text: string): string => wrap("32", text);
export const yellow = (text: string): string => wrap("33", text);
export const cyan = (text: string): string => wrap("36", text);
export const gray = (text: string): string => wrap("90", text);

// Text styles
export const bold = (text: string): string => wrap("1", text);
export const dim = (text: string): string => wrap("2", text);
