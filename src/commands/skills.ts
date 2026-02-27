import type { SkillState } from "../core/skills.ts";
import { getSkillsStatus, installSkills, listSkills } from "../core/skills.ts";
import type { CommandResult } from "../types/index.ts";
import { dim, green, yellow } from "../ui/colors.ts";

export function runSkillsInstall(): CommandResult {
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

export function runSkillsStatus(): CommandResult {
	const statuses = getSkillsStatus();
	console.log();
	for (const agentStatus of statuses) {
		if (!agentStatus.available) {
			console.log(`${dim(agentStatus.agent)}  ${dim("(not installed)")}`);
			continue;
		}
		console.log(agentStatus.agent);
		for (const skill of agentStatus.skills) {
			const { icon, label } = skillStateDisplay(skill.state);
			console.log(`  ${icon} ${skill.name}  ${label}`);
		}
	}
	console.log();
	return { exitCode: 0 };
}

export function runSkillsList(): CommandResult {
	const skills = listSkills();
	console.log(`\nBundled skills (${skills.length}):\n`);
	for (const skill of skills) {
		console.log(`  ${green(skill.name)}`);
		console.log(`    ${dim(skill.description)}\n`);
	}
	return { exitCode: 0 };
}
