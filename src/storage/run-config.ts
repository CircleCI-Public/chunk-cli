import * as fs from "node:fs";
import * as path from "node:path";
import { z } from "zod";

// Zod schemas for validation
const RunDefinitionSchema = z.object({
	definition_id: z.string().uuid("definition_id must be a valid UUID"),
	description: z.string().optional(),
	chunk_environment_id: z
		.string()
		.uuid("chunk_environment_id must be a valid UUID")
		.nullable()
		.optional(),
	default_branch: z.string().optional(),
});

const RunConfigSchema = z.object({
	org_id: z.string().min(1, "org_id cannot be empty"),
	project_id: z.string().min(1, "project_id cannot be empty"),
	org_type: z.enum(["github", "circleci"]).default("github"),
	definitions: z.record(z.string(), RunDefinitionSchema),
});

export type RunDefinition = z.infer<typeof RunDefinitionSchema>;
export type RunConfig = z.infer<typeof RunConfigSchema>;

/**
 * Find the git repository root by walking up the directory tree
 */
function findRepoRoot(): string {
	let dir = process.cwd();
	const root = path.parse(dir).root;

	while (dir !== root) {
		if (fs.existsSync(path.join(dir, ".git"))) {
			return dir;
		}
		dir = path.dirname(dir);
	}

	throw new Error("Not in a git repository");
}

/**
 * Get the path to the run config file (.chunk/run.json at repo root)
 */
export function getRunConfigPath(): string {
	const repoRoot = findRepoRoot();
	return path.join(repoRoot, ".chunk", "run.json");
}

/**
 * Load and validate the run configuration
 */
export function loadRunConfig(): RunConfig {
	const configPath = getRunConfigPath();

	if (!fs.existsSync(configPath)) {
		throw new Error("Run configuration not found");
	}

	const content = fs.readFileSync(configPath, "utf-8");

	let parsed: unknown;
	try {
		parsed = JSON.parse(content);
	} catch (error) {
		throw new Error(
			`Invalid JSON in run configuration: ${error instanceof Error ? error.message : String(error)}`,
		);
	}

	return validateRunConfig(parsed);
}

/**
 * Validate run configuration using Zod schema
 */
export function validateRunConfig(config: unknown): RunConfig {
	try {
		return RunConfigSchema.parse(config);
	} catch (error) {
		if (error instanceof z.ZodError) {
			const issues = error.issues
				.map((issue) => `${issue.path.join(".")}: ${issue.message}`)
				.join(", ");
			throw new Error(`Invalid run configuration: ${issues}`);
		}
		throw error;
	}
}

/**
 * Save run configuration to .chunk/run.json
 */
export function saveRunConfig(config: RunConfig): void {
	const configPath = getRunConfigPath();
	const configDir = path.dirname(configPath);

	// Create .chunk directory if it doesn't exist
	if (!fs.existsSync(configDir)) {
		fs.mkdirSync(configDir, { recursive: true, mode: 0o755 });
	}

	const content = JSON.stringify(config, null, 2);
	fs.writeFileSync(configPath, content, { mode: 0o644 });
}

/**
 * Get definition by name or treat as UUID
 * Returns the definition_id, chunk_environment_id, and default_branch
 */
export function getDefinitionByNameOrId(
	config: RunConfig,
	nameOrId: string,
): { definitionId: string; envId?: string | null; branch: string } {
	// First, try to look up by name
	const definition = config.definitions[nameOrId];

	if (definition) {
		return {
			definitionId: definition.definition_id,
			envId: definition.chunk_environment_id,
			branch: definition.default_branch || "main",
		};
	}

	// Otherwise, treat as a UUID and validate format
	const uuidRegex = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
	if (!uuidRegex.test(nameOrId)) {
		throw new Error(`Unknown definition name: ${nameOrId}`);
	}

	// Use the UUID directly as definition_id
	return {
		definitionId: nameOrId,
		envId: undefined,
		branch: "main",
	};
}
