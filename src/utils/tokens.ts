/**
 * Resolve the CircleCI API token from environment variables.
 * Prefers CIRCLE_TOKEN but falls back to CIRCLECI_TOKEN.
 */
export function getCircleCIToken(): string | undefined {
	return process.env.CIRCLE_TOKEN ?? process.env.CIRCLECI_TOKEN;
}
