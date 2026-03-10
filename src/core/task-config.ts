import * as fs from "node:fs";
import {
	type CircleCICollaboration,
	CircleCIError,
	type CircleCIProject,
	fetchCollaborations,
	fetchFollowedProjects,
	fetchProjectBySlug,
} from "../api/circleci";
import { getCircleCIToken } from "../config";
import {
	getRunConfigPath,
	type RunConfig,
	type RunDefinition,
	saveRunConfig,
	validateRunConfig,
} from "../storage/run-config";
import type { CommandResult } from "../types";
import { bold, cyan, dim, yellow } from "../ui/colors";
import { formatStep, label, printSuccess, printWarning } from "../ui/format";
import { promptConfirm, promptInput, promptSelect } from "../ui/prompt";
import { handleError, printError } from "../utils/errors";

const UUID_REGEX = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function isValidUuid(value: string): boolean {
	return UUID_REGEX.test(value);
}

async function promptRequiredInput(message: string): Promise<string> {
	while (true) {
		const value = (await promptInput(message)).trim();
		if (value) return value;
		console.log(yellow("  This field is required."));
	}
}

async function promptUuid(message: string, required: boolean): Promise<string | null> {
	while (true) {
		const value = (await promptInput(message)).trim();
		if (!value && !required) return null;
		if (!value && required) {
			console.log(yellow("  This field is required."));
			continue;
		}
		if (!isValidUuid(value)) {
			console.log(yellow("  Must be a valid UUID (e.g. 550e8400-e29b-41d4-a716-446655440000)."));
			continue;
		}
		return value;
	}
}

const ANOTHER_PROJECT_SENTINEL = Symbol("another-project");
type ProjectSelection = CircleCIProject | typeof ANOTHER_PROJECT_SENTINEL;

export function mapVcsTypeToOrgType(vcsType: string | undefined): "github" | "circleci" {
	const lower = vcsType?.toLowerCase();
	if (lower === "github" || lower === "gh") return "github";
	return "circleci";
}

export function buildProjectSlug(vcsType: string | undefined, org: string, repo: string): string {
	const prefix = vcsType?.toLowerCase() === "bitbucket" ? "bb" : "gh";
	return `${prefix}/${org}/${repo}`;
}

export function sortProjectsByName(projects: CircleCIProject[]): CircleCIProject[] {
	return [...projects].sort((a, b) =>
		`${a.username}/${a.reponame}`.localeCompare(`${b.username}/${b.reponame}`),
	);
}

async function collectDefinition(): Promise<{ name: string; definition: RunDefinition }> {
	const name = await promptRequiredInput("  Definition name (e.g. dev, prod): ");

	const definitionId = await promptUuid("  Definition ID (UUID from CircleCI): ", true);
	if (!definitionId) throw new Error("definition_id is required");

	const description = (await promptInput("  Description (optional): ")).trim() || undefined;
	const branchInput = (await promptInput("  Default branch [main]: ")).trim();
	const defaultBranch = branchInput || "main";
	const envId = await promptUuid("  Environment ID (optional UUID): ", false);

	const definition: RunDefinition = {
		definition_id: definitionId,
		...(description && { description }),
		default_branch: defaultBranch,
		...(envId !== null && { chunk_environment_id: envId }),
	};

	return { name, definition };
}

export async function runTaskConfigWizard(): Promise<CommandResult> {
	console.log(`\n${bold("Chunk Run Setup")}\n`);
	console.log(`This wizard creates ${cyan(".chunk/run.json")} in your repository root.`);
	console.log("");

	// Check for CircleCI token
	const token = getCircleCIToken();
	if (!token) {
		printError(
			"CircleCI token not found",
			"CIRCLE_TOKEN environment variable is required for setup.",
			"Export your CircleCI personal API token:\n  export CIRCLE_TOKEN=<your-token>",
		);
		return { exitCode: 2 };
	}

	// Check for git repo and existing config
	let configPath: string;
	try {
		configPath = getRunConfigPath();
	} catch (error) {
		const err = error instanceof Error ? error : new Error(String(error));
		printError(
			"Not in a git repository",
			err.message,
			"Run this command from within your project.",
		);
		return { exitCode: 2 };
	}

	if (fs.existsSync(configPath)) {
		printWarning(`${configPath} already exists.`);
		const overwrite = await promptConfirm("Overwrite the existing configuration?");
		if (!overwrite) {
			console.log("\nSetup cancelled.\n");
			return { exitCode: 0 };
		}
		console.log("");
	}

	// Step 1: Organization & project via API
	console.log(`${formatStep(1, 3, "Organization & project")}\n`);
	console.log(dim("  Fetching your CircleCI projects...\n"));

	let projects: CircleCIProject[];
	let collaborations: CircleCICollaboration[];
	try {
		[projects, collaborations] = await Promise.all([
			fetchFollowedProjects(token),
			fetchCollaborations(token),
		]);
	} catch (error) {
		if (error instanceof CircleCIError) {
			printError(
				"Failed to fetch CircleCI data",
				error.message,
				"Check your CIRCLE_TOKEN and try again.",
			);
			return { exitCode: 2 };
		}
		handleError(error);
		return { exitCode: 2 };
	}

	let orgId: string;
	let projectId: string;
	let orgType: "github" | "circleci" = "github";

	if (projects.length === 0) {
		console.log(dim("  No followed projects found. Enter IDs manually.\n"));

		// Fall back to org selection from collaborations
		if (collaborations.length > 0) {
			const selectedOrg = await promptSelect<CircleCICollaboration>(
				"\nSelect your organization:",
				collaborations,
				(collab) => collab.name,
			);
			orgId = selectedOrg.id;
			orgType = mapVcsTypeToOrgType(selectedOrg["vcs-type"]);
			console.log("");
		} else {
			orgId = await promptRequiredInput("Organization ID: ");
			orgType = "github";
		}
		projectId = await promptRequiredInput("Enter a project ID: ");
	} else {
		// Build selection list sorted alphabetically, plus "Another project" sentinel
		const sortedProjects = sortProjectsByName(projects);
		const selectionItems: ProjectSelection[] = [...sortedProjects, ANOTHER_PROJECT_SENTINEL];

		const selected = await promptSelect<ProjectSelection>(
			"\nSelect a project:",
			selectionItems,
			(item) => {
				if (item === ANOTHER_PROJECT_SENTINEL) {
					return "Another project (enter IDs manually)";
				}
				return `${item.username}/${item.reponame}`;
			},
		);

		if (selected === ANOTHER_PROJECT_SENTINEL) {
			// User wants to enter IDs manually — show org list if available
			if (collaborations.length > 0) {
				const selectedOrg = await promptSelect<CircleCICollaboration>(
					"\nSelect your organization:",
					collaborations,
					(collab) => collab.name,
				);
				orgId = selectedOrg.id;
				orgType = mapVcsTypeToOrgType(selectedOrg["vcs-type"]);
			} else {
				orgId = await promptRequiredInput("\nOrganization ID: ");
				orgType = "github";
			}
			console.log("");
			projectId = await promptRequiredInput("Enter a project ID: ");
		} else {
			// Resolve UUIDs from selected project
			const slug = buildProjectSlug(selected.vcs_type, selected.username, selected.reponame);
			console.log(dim(`\n  Resolving project details for ${slug}...\n`));

			let projectDetails: Awaited<ReturnType<typeof fetchProjectBySlug>>;
			try {
				projectDetails = await fetchProjectBySlug(token, slug);
			} catch (error) {
				if (error instanceof CircleCIError) {
					printError(
						"Failed to fetch project details",
						error.message,
						"Check your CIRCLE_TOKEN and try again.",
					);
					return { exitCode: 2 };
				}
				handleError(error);
				return { exitCode: 2 };
			}

			projectId = projectDetails.id;
			orgId = projectDetails.organization_id;

			// Determine org type from the project's vcs_type, falling back to collaboration data
			if (selected.vcs_type) {
				orgType = mapVcsTypeToOrgType(selected.vcs_type);
			} else {
				const matchedCollab = collaborations.find(
					(c) => c.name.toLowerCase() === selected.username.toLowerCase(),
				);
				orgType = mapVcsTypeToOrgType(matchedCollab?.["vcs-type"]);
			}
		}
	}

	const labelWidth = 18;
	console.log(dim("\n  Resolved configuration:"));
	console.log(`  ${label("Org ID:", labelWidth)} ${orgId}`);
	console.log(`  ${label("Project ID:", labelWidth)} ${projectId}`);
	console.log(`  ${label("Org type:", labelWidth)} ${orgType}\n`);

	// Collect definitions
	console.log(`\n${formatStep(2, 3, "Pipeline definitions")}\n`);
	console.log(
		dim(
			"  A definition maps a short name (e.g. dev, prod) to a CircleCI chunk pipeline definition.\n" +
				"  Find the definition UUID in CircleCI → your project → the chunk pipeline definition page.\n",
		),
	);

	const definitions: Record<string, RunDefinition> = {};

	do {
		const { name, definition } = await collectDefinition();
		if (definitions[name]) {
			printWarning(`Definition "${name}" already exists — overwriting.`);
		}
		definitions[name] = definition;
		console.log("");
	} while (await promptConfirm("Add another definition?"));

	// Validate and save
	console.log(`\n${formatStep(3, 3, "Saving configuration")}\n`);

	const rawConfig = { org_id: orgId, project_id: projectId, org_type: orgType, definitions };

	let config: RunConfig;
	try {
		config = validateRunConfig(rawConfig);
	} catch (error) {
		const err = error instanceof Error ? error : new Error(String(error));
		printError("Invalid configuration", err.message);
		return { exitCode: 2 };
	}

	try {
		saveRunConfig(config);
	} catch (error) {
		const err = error instanceof Error ? error : new Error(String(error));
		printError("Failed to save configuration", err.message);
		return { exitCode: 2 };
	}

	printSuccess("Configuration saved!");
	console.log(dim(`  ${configPath}\n`));

	const defNames = Object.keys(definitions).join(", ");
	console.log(`${bold("Next steps:")}`);
	console.log("  Run a task with:");
	console.log(
		dim(
			`  chunk task run --definition ${Object.keys(definitions)[0] ?? "<definition>"} --prompt "your task"\n`,
		),
	);
	if (Object.keys(definitions).length > 1) {
		console.log(dim(`  Available definitions: ${defNames}\n`));
	}

	return { exitCode: 0 };
}
