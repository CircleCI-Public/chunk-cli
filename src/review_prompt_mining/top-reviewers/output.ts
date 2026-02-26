import Table from "cli-table3";
import { bold, dim } from "../../ui/colors";
import { label } from "../../ui/format";
import type { PRRankingRow, ReviewCommentDetail, UserActivity } from "../types";

export interface OutputMetadata {
	org: string;
	since?: string; // Optional - not used in PR-count mode
	prCountPerRepo?: number; // Optional - used in PR-count mode
	totalPRsAnalyzed?: number; // Optional - used in PR-count mode
	analyzedAt: string;
	totalRepos: number;
	totalContributors: number;
}

// Print results as a formatted table
export function printTable(activities: UserActivity[], metadata: OutputMetadata): void {
	// Align with the longest label ("Organization:")
	const labelWidth = 15;

	console.log("");
	console.log(`  ${bold("Top Contributors by PR Review Activity")}`);
	console.log(`  ${label("Organization:", labelWidth)} ${metadata.org}`);
	if (metadata.since) {
		console.log(
			`  ${label("Time range:", labelWidth)} ${metadata.since} to ${metadata.analyzedAt}`,
		);
	} else if (metadata.prCountPerRepo) {
		console.log(
			`  ${label("PRs per repo:", labelWidth)} ${metadata.prCountPerRepo} (analyzed at: ${metadata.analyzedAt})`,
		);
		if (metadata.totalPRsAnalyzed !== undefined) {
			console.log(`  ${label("PRs analyzed:", labelWidth)} ${metadata.totalPRsAnalyzed}`);
		}
	}
	console.log(`  ${label("Repos:", labelWidth)} ${metadata.totalRepos}`);
	console.log("");

	if (activities.length === 0) {
		console.log(dim("  No review activity found."));
		return;
	}

	const table = new Table({
		head: ["Rank", "User", "Total", "Approvals", "Changes Req", "Comments", "Repos"],
		style: { head: ["cyan"] },
	});

	activities.forEach((activity, index) => {
		table.push([
			index + 1,
			activity.login,
			activity.totalActivity,
			activity.approvals,
			activity.changesRequested,
			activity.reviewComments,
			activity.reposActiveIn.size,
		]);
	});

	const tableStr = table.toString();
	const indentedTable = tableStr
		.split("\n")
		.map((line) => `  ${line}`)
		.join("\n");
	console.log(indentedTable);
}

// Write comment details to JSON file
export async function writeDetailsJSON(
	details: ReviewCommentDetail[],
	outputPath: string,
	metadata: OutputMetadata,
): Promise<void> {
	const outputMetadata: Record<string, unknown> = {
		organization: metadata.org,
		analyzedAt: metadata.analyzedAt,
		totalReposAnalyzed: metadata.totalRepos,
		totalComments: details.length,
	};

	// Include either time-based or PR-count-based fields
	if (metadata.since) {
		outputMetadata.since = metadata.since;
	}
	if (metadata.prCountPerRepo !== undefined) {
		outputMetadata.prCountPerRepo = metadata.prCountPerRepo;
	}
	if (metadata.totalPRsAnalyzed !== undefined) {
		outputMetadata.totalPRsAnalyzed = metadata.totalPRsAnalyzed;
	}

	const output = {
		metadata: outputMetadata,
		comments: details,
	};

	await Bun.write(outputPath, JSON.stringify(output, null, 2));
}

// Derive PR rankings CSV path from details JSON path
export function derivePRRankingsCSVPath(detailsOutputPath: string): string {
	const ext = ".json";
	if (detailsOutputPath.endsWith(ext)) {
		return `${detailsOutputPath.slice(0, -ext.length)}-pr-rankings.csv`;
	}
	return `${detailsOutputPath}-pr-rankings.csv`;
}

// Aggregate comments by PR and generate rankings sorted by total comments
export function aggregatePRRankings(details: ReviewCommentDetail[]): PRRankingRow[] {
	const prMap = new Map<
		string,
		{
			repo: string;
			number: number;
			title: string;
			author: string;
			url: string;
			state: string;
			comments: number;
			reviewers: Set<string>;
		}
	>();

	for (const detail of details) {
		const key = `${detail.pr.repo}/${detail.pr.number}`;
		const existing = prMap.get(key);

		if (existing) {
			existing.comments++;
			existing.reviewers.add(detail.reviewer);
		} else {
			prMap.set(key, {
				repo: detail.pr.repo,
				number: detail.pr.number,
				title: detail.pr.title,
				author: detail.pr.author,
				url: detail.pr.url,
				state: detail.pr.state,
				comments: 1,
				reviewers: new Set([detail.reviewer]),
			});
		}
	}

	// Sort by total_comments descending and add rank
	return Array.from(prMap.values())
		.sort((a, b) => b.comments - a.comments)
		.map((pr, index) => ({
			rank: index + 1,
			repo: pr.repo,
			pr_number: pr.number,
			pr_title: pr.title,
			pr_author: pr.author,
			pr_url: pr.url,
			total_comments: pr.comments,
			reviewer_count: pr.reviewers.size,
			state: pr.state,
		}));
}

const PR_RANKINGS_CSV_COLUMNS: (keyof PRRankingRow)[] = [
	"rank",
	"repo",
	"pr_number",
	"pr_title",
	"pr_author",
	"total_comments",
	"reviewer_count",
	"state",
	"pr_url",
];

function escapeCSV(value: string | number): string {
	const strValue = String(value);
	if (
		strValue.includes(",") ||
		strValue.includes('"') ||
		strValue.includes("\n") ||
		strValue.includes("\r")
	) {
		return `"${strValue.replace(/"/g, '""')}"`;
	}
	return strValue;
}

// Write PR rankings to CSV file
export async function writePRRankingsCSV(
	rankings: PRRankingRow[],
	outputPath: string,
): Promise<void> {
	const header = PR_RANKINGS_CSV_COLUMNS.join(",");
	const csvRows = rankings.map((row) =>
		PR_RANKINGS_CSV_COLUMNS.map((col) => escapeCSV(row[col])).join(","),
	);

	const content = `${[header, ...csvRows].join("\n")}\n`;
	await Bun.write(outputPath, content);
}
