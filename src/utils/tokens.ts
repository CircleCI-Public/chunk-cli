import { printError } from "./errors";

/**
 * Resolve the CircleCI API token from environment variables.
 * Prefers CIRCLE_TOKEN but falls back to CIRCLECI_TOKEN.
 */
export function getCircleCIToken(): string | undefined {
	return process.env.CIRCLE_TOKEN ?? process.env.CIRCLECI_TOKEN;
}

export function requireToken(): string | null {
	const token = getCircleCIToken();
	if (!token) {
		printError(
			"CircleCI token not found",
			"CIRCLE_TOKEN environment variable is not set.",
			"Set CIRCLE_TOKEN to your CircleCI personal API token.",
		);
		return null;
	}
	return token;
}
