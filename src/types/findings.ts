import { z } from "zod";

/**
 * Line reference can be a single line number or a range
 */
export const LineRangeSchema = z.union([
	z.number().int().positive(),
	z.object({
		start: z.number().int().positive(),
		end: z.number().int().positive(),
	}),
]);

export type LineRange = z.infer<typeof LineRangeSchema>;

/**
 * Optional code fix with original and fixed versions
 */
export const CodeFixSchema = z
	.object({
		original: z.string(),
		fixed: z.string(),
	})
	.optional();

export type CodeFix = z.infer<typeof CodeFixSchema>;

/**
 * Severity levels for findings
 */
export const SeveritySchema = z.enum(["CRITICAL", "MAJOR", "MINOR", "NITPICK"]);

export type Severity = z.infer<typeof SeveritySchema>;

/**
 * Optional category for findings
 */
export const CategorySchema = z.string().optional();

export type Category = z.infer<typeof CategorySchema>;

/**
 * Individual finding from code review
 */
export const FindingSchema = z.object({
	severity: SeveritySchema,
	file: z.string(),
	line: LineRangeSchema.optional(),
	issue: z.string(),
	suggestion: z.string(),
	codeFix: CodeFixSchema,
	category: CategorySchema,
});

export type Finding = z.infer<typeof FindingSchema>;

/**
 * Complete review response with findings array
 */
export const ReviewResponseSchema = z.object({
	findings: z.array(FindingSchema),
	summary: z.string().optional(),
});

export type ReviewResponse = z.infer<typeof ReviewResponseSchema>;

/**
 * Helper to format line range as string
 */
export function formatLineRange(line?: LineRange): string {
	if (!line) return "";
	if (typeof line === "number") return `:${line}`;
	return `:${line.start}-${line.end}`;
}

/**
 * Helper to get location string (file:line)
 */
export function getLocation(finding: Finding): string {
	return `${finding.file}${formatLineRange(finding.line)}`;
}
