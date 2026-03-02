// Skill content is imported at build time. Bun's text loader inlines the file
// content as a string literal in the compiled binary (requires --loader .md:text
// in build commands).
import chunkReviewContent from "../../skills/chunk-review/SKILL.md";

export interface EmbeddedSkill {
	name: string;
	description: string;
	content: string;
}

export const EMBEDDED_SKILLS: readonly EmbeddedSkill[] = [
	{
		name: "chunk-review",
		description:
			'Use when asked to "review recent changes", "chunk review", "review my diff", "review this PR", or "review my changes". Applies team-specific review standards from .chunk/review-prompt.md.',
		content: chunkReviewContent,
	},
];
