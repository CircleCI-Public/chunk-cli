import type { Command } from "@commander-js/extra-typings";
import type { SkillState } from "../core/skills.ts";
import { getSkillsStatus, installSkills } from "../core/skills.ts";
import type { CommandResult } from "../types/index.ts";
import { dim, green, yellow } from "../ui/colors.ts";

export function registerSkillsCommands(program: Command): void {
	const skills = program.command("skills").description("Install and manage AI agent skills");
	skills
		.command("install")
		.description("Install or update all skills into agent config directories")
		.action(() => process.exit(runSkillsInstall().exitCode));
	skills
		.command("list")
		.description("List bundled skills and their per-agent installation status")
		.action(() => process.exit(runSkillsList().exitCode));
}

function runSkillsInstall(): CommandResult {
	const results = installSkills();

	for (const result of results) {
		if (result.skipped) {
			console.log(dim(`${result.agent}: skipped (not installed)`));
			continue;
		}
		if (result.installed.length === 0 && result.updated.length === 0) {
			console.log(`${result.agent}: ${green("all skills up to date")}`);
			continue;
		}
		for (const name of result.installed) {
			console.log(`${result.agent}: ${green(`installed ${name}`)}`);
		}
		for (const name of result.updated) {
			console.log(`${result.agent}: ${yellow(`updated ${name}`)}`);
		}
	}

	return { exitCode: 0 };
}

function skillStateDisplay(state: SkillState): { icon: string; label: string } {
	if (state === "current") return { icon: green("✓"), label: green("current") };
	if (state === "outdated") return { icon: yellow("⚠"), label: yellow("outdated") };
	return { icon: dim("✗"), label: dim("missing") };
}

export function runSkillsList(): CommandResult {
	const statuses = getSkillsStatus();

	// Collect unique skill names (same across all agents).
	const skillNames = statuses[0]?.skills.map((s) => s.name) ?? [];

	console.log(`\nBundled skills (${skillNames.length}):\n`);

	for (const name of skillNames) {
		// Grab description from first agent entry (identical across agents).
		const description = statuses[0]?.skills.find((s) => s.name === name)?.description ?? "";
		console.log(`  ${green(name)}`);
		console.log(`    ${dim(description)}`);

		for (const agentStatus of statuses) {
			const skill = agentStatus.skills.find((s) => s.name === name);
			if (!skill) continue;

			if (!agentStatus.available) {
				console.log(`      ${dim(agentStatus.agent)}: ${dim("n/a (agent not installed)")}`);
			} else {
				const { icon, label } = skillStateDisplay(skill.state);
				console.log(`      ${agentStatus.agent}: ${icon} ${label}`);
			}
		}
		console.log();
	}

	return { exitCode: 0 };
}
