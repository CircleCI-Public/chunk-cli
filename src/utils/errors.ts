import { red } from "../ui/colors";

/**
 * Format an error message in the standard format:
 * ✗ Error: <brief>
 *
 * <detail>
 *
 * Suggestion: <action>
 */
export function formatError(brief: string, detail?: string, suggestion?: string): string {
	const lines: string[] = [];

	lines.push(red(`✗ Error: ${brief}`));
	lines.push("");

	if (detail) {
		lines.push(detail);
		lines.push("");
	}

	if (suggestion) {
		lines.push(`Suggestion: ${suggestion}`);
	}

	return lines.join("\n");
}

/**
 * Print an error message to stderr in the standard format
 */
export function printError(brief: string, detail?: string, suggestion?: string): void {
	console.error(formatError(brief, detail, suggestion));
}

/**
 * Determine if an error is a network error
 */
export function isNetworkError(error: Error): boolean {
	const message = error.message.toLowerCase();
	const networkPatterns = [
		"network",
		"fetch failed",
		"econnrefused",
		"econnreset",
		"etimedout",
		"enotfound",
		"unable to connect",
		"internet",
		"socket hang up",
		"failed to fetch",
	];
	return networkPatterns.some((pattern) => message.includes(pattern));
}

/**
 * Determine if an error is an authentication error
 */
export function isAuthError(error: Error): boolean {
	const message = error.message.toLowerCase();
	const authPatterns = [
		"api key",
		"authentication",
		"unauthorized",
		"invalid credentials",
		"auth",
		"401",
	];
	return authPatterns.some((pattern) => message.includes(pattern));
}

/**
 * Get a suggestion based on error type
 */
function getSuggestion(error: Error): string {
	if (isNetworkError(error)) {
		return "Check your internet connection and try again.";
	}
	if (isAuthError(error)) {
		return "Run `chunk auth login` to set up your API key.";
	}
	return "Check the error details and try again.";
}

/**
 * Handle an error and print a formatted message.
 * Returns exit code 2.
 */
export function handleError(
	error: unknown,
	context?: { brief?: string; detail?: string; suggestion?: string },
): void {
	const err = error instanceof Error ? error : new Error(String(error));

	const brief = context?.brief ?? err.message;
	const detail = context?.detail;
	let suggestion = context?.suggestion;

	// Auto-detect suggestion based on error type if not provided
	if (!suggestion) {
		suggestion = getSuggestion(err);
	}

	printError(brief, detail, suggestion);
}
