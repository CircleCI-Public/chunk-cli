import * as fs from "node:fs";
import * as path from "node:path";
import { findRepoRoot } from "./run-config";

export interface ProjectConfig {
	installCommand?: string;
	testCommand?: string;
}

export function getProjectConfigPath(): string {
	return path.join(findRepoRoot(), ".chunk", "config.json");
}

export function loadProjectConfig(): ProjectConfig {
	const configPath = getProjectConfigPath();

	if (!fs.existsSync(configPath)) {
		return {};
	}

	try {
		const content = fs.readFileSync(configPath, "utf-8");
		const parsed = JSON.parse(content) as Record<string, unknown>;
		return {
			installCommand: typeof parsed.installCommand === "string" ? parsed.installCommand : undefined,
			testCommand: typeof parsed.testCommand === "string" ? parsed.testCommand : undefined,
		};
	} catch {
		return {};
	}
}

export function saveProjectConfig(config: ProjectConfig): void {
	const configPath = getProjectConfigPath();
	const configDir = path.dirname(configPath);

	if (!fs.existsSync(configDir)) {
		fs.mkdirSync(configDir, { recursive: true, mode: 0o755 });
	}

	const existing = loadProjectConfig();
	const merged = { ...existing, ...config };
	fs.writeFileSync(configPath, `${JSON.stringify(merged, null, 2)}\n`, { mode: 0o644 });
}
