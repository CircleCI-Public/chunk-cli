/**
 * Template files for `chunk hook repo init`.
 *
 * Each template is a real file under `packages/hook/templates/` imported at
 * build time via `with { type: "text" }`. Bun embeds the file contents into
 * the compiled binary — no disk access at runtime.
 *
 * JSON templates are imported as objects (via `resolveJsonModule`) and
 * stringified back to text for the manifest.
 *
 * The `.claude/settings.json` template contains `__PROJECT__` placeholders
 * that are substituted with the repo's basename at init time.
 */

import configContent from "../../templates/.chunk/hook/config.yml" with { type: "text" };
import unifiedConfigContent from "../../templates/.chunk/config.yml" with { type: "text" };
import gitignoreContent from "../../templates/.chunk/hook/gitignore" with { type: "text" };
import settingsObj from "../../templates/.claude/settings.json";

// ---------------------------------------------------------------------------
// Template manifest — files used by `chunk hook repo init`
// ---------------------------------------------------------------------------

/** A template file descriptor. */
export type TemplateFile = {
	/** Relative path within the target repo (e.g. ".chunk/hook/config.yml"). */
	relativePath: string;
	/** Raw file content. For settings.json, contains __PROJECT__ placeholders. */
	content: string;
	/** Whether __PROJECT__ substitution should be applied. */
	substituteProject: boolean;
};

/** All template files copied by `chunk hook repo init`. */
export const TEMPLATE_FILES: TemplateFile[] = [
	{
		relativePath: ".chunk/hook/.gitignore",
		content: gitignoreContent,
		substituteProject: false,
	},
	{
		relativePath: ".chunk/config.yml",
		content: unifiedConfigContent,
		substituteProject: false,
	},
	{
		relativePath: ".chunk/hook/config.yml",
		content: configContent,
		substituteProject: false,
	},
	{
		relativePath: ".claude/settings.json",
		content: `${JSON.stringify(settingsObj, null, 2)}
`,
		substituteProject: true,
	},
];
