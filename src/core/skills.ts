import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { EMBEDDED_SKILLS, type EmbeddedSkill } from "../skills/index.ts";

export type SkillState = "missing" | "current" | "outdated";

interface AgentTarget {
	type: string;
	/** Parent config dir — must exist for skills to be installed. */
	configDir: string;
	/** Where individual skill subdirectories live. */
	skillsDir: string;
}

export interface InstallResult {
	agent: string;
	installed: string[];
	updated: string[];
	/** true when the agent's config dir doesn't exist — nothing was done. */
	skipped: boolean;
}

export interface SkillStatus {
	name: string;
	description: string;
	state: SkillState;
}

export interface AgentStatus {
	agent: string;
	/** false when the agent's config directory doesn't exist on this machine. */
	available: boolean;
	skills: SkillStatus[];
}

const home = process.env.HOME ?? process.env.USERPROFILE ?? "~";

const AGENTS: AgentTarget[] = [
	{
		type: "claude",
		configDir: join(home, ".claude"),
		skillsDir: join(home, ".claude", "skills"),
	},
	{
		type: "codex",
		configDir: join(home, ".codex"),
		skillsDir: join(home, ".codex", "skills"),
	},
];

function skillFilePath(skillsDir: string, skill: EmbeddedSkill): string {
	return join(skillsDir, skill.name, "SKILL.md");
}

function skillState(skillsDir: string, skill: EmbeddedSkill): SkillState {
	const path = skillFilePath(skillsDir, skill);
	if (!existsSync(path)) return "missing";
	const existing = readFileSync(path, "utf-8");
	return existing === skill.content ? "current" : "outdated";
}

function installForAgent(agent: AgentTarget): InstallResult {
	if (!existsSync(agent.configDir)) {
		return { agent: agent.type, installed: [], updated: [], skipped: true };
	}

	const installed: string[] = [];
	const updated: string[] = [];

	for (const skill of EMBEDDED_SKILLS) {
		const state = skillState(agent.skillsDir, skill);
		if (state === "current") continue;

		const destDir = join(agent.skillsDir, skill.name);
		mkdirSync(destDir, { recursive: true });
		writeFileSync(skillFilePath(agent.skillsDir, skill), skill.content, "utf-8");

		if (state === "missing") {
			installed.push(skill.name);
		} else {
			updated.push(skill.name);
		}
	}

	return { agent: agent.type, installed, updated, skipped: false };
}

/**
 * Install embedded skills for all agents whose config directories exist.
 * Idempotent — re-running updates outdated skills and skips current ones.
 */
export function installSkills(): InstallResult[] {
	return AGENTS.map(installForAgent);
}

/**
 * Return per-agent, per-skill installation state without modifying anything.
 */
export function getSkillsStatus(): AgentStatus[] {
	return AGENTS.map((agent) => {
		const available = existsSync(agent.configDir);
		const skills = EMBEDDED_SKILLS.map((skill) => ({
			name: skill.name,
			description: skill.description,
			state: available ? skillState(agent.skillsDir, skill) : ("missing" as SkillState),
		}));
		return { agent: agent.type, available, skills };
	});
}

/** Return the list of skills embedded in this binary. */
export function listSkills(): { name: string; description: string }[] {
	return EMBEDDED_SKILLS.map(({ name, description }) => ({ name, description }));
}
