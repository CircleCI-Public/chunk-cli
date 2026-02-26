export interface ReportMetadata {
	inputFile: string;
	totalComments: number;
	reviewers: string[];
	analyzedAt: string;
}

/**
 * Format analysis as markdown report
 */
export function formatMarkdownReport(analysis: string, metadata: ReportMetadata): string {
	return `# Code Review Pattern Analysis

**Generated:** ${metadata.analyzedAt}
**Source:** ${metadata.inputFile}
**Total Comments:** ${metadata.totalComments}
**Reviewers:** ${metadata.reviewers.join(", ")}

---

${analysis}

---

*This analysis was generated using Claude AI (Sonnet 4.5) by analyzing code review patterns.*
`;
}
