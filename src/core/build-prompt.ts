import { mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import {
	analyzeReviews,
	createClaudeClient as createAnalyzeClient,
	isTokenLimitError,
} from "../review_prompt_mining/analyze/claude-client";
import {
	groupByReviewer,
	limitCommentsPerReviewer,
	parseInputJSON,
} from "../review_prompt_mining/analyze/json-parser";
import {
	buildAnalysisPrompt,
	estimateTokenCount,
} from "../review_prompt_mining/analyze/prompt-builder";
import { formatMarkdownReport } from "../review_prompt_mining/analyze/report-formatter";
import {
	createClaudeClient as createPromptClient,
	generateReviewPrompt,
} from "../review_prompt_mining/generate-prompt/claude-prompt-generator";
import {
	checkRateLimit,
	createGraphQLClient,
	validateOrgAccess,
} from "../review_prompt_mining/graphql-client";
import {
	aggregateActivity,
	aggregateDetails,
	topN,
} from "../review_prompt_mining/top-reviewers/aggregator";
import {
	aggregatePRRankings,
	derivePRRankingsCSVPath,
	printTable,
	writeDetailsJSON,
	writePRRankingsCSV,
} from "../review_prompt_mining/top-reviewers/output";
import { fetchOrgRepos } from "../review_prompt_mining/top-reviewers/repo-iterator";
import { fetchReviewActivity } from "../review_prompt_mining/top-reviewers/review-fetcher";
import type { ReviewCommentDetail, UserActivity } from "../review_prompt_mining/types";
import { bold, dim, yellow } from "../ui/colors";
import { formatStep, formatSuccess, label, printSuccess } from "../ui/format";
import { TerminalSpinner } from "../ui/spinner";

export interface BuildPromptOptions {
	org: string;
	repos: string[];
	top: number;
	since: Date;
	outputPath: string;
	maxComments?: number;
	analyzeModel: string;
	promptModel: string;
	includeAttribution: boolean;
}

export async function extractCommentsAndBuildPrompt(options: BuildPromptOptions): Promise<void> {
	const {
		org,
		repos,
		top,
		since,
		outputPath,
		maxComments,
		analyzeModel,
		promptModel,
		includeAttribution,
	} = options;

	// Derive intermediate file paths alongside the final output
	const outputBase = outputPath.replace(/\.md$/, "");
	const detailsPath = `${outputBase}-details.json`;
	const analysisPath = `${outputBase}-analysis.md`;
	const sinceStr = since.toISOString().slice(0, 10);
	const spinner = new TerminalSpinner();

	// Align with the longest label ("Top reviewers:")
	const labelWidth = 15;

	console.log(bold("Chunk CLI - Build PR Review Prompt"));
	console.log("");
	console.log(`  ${label("Organization:", labelWidth)} ${org}`);
	console.log(`  ${label("Repos:", labelWidth)} ${repos.length > 0 ? repos.join(", ") : "all"}`);
	console.log(`  ${label("Top reviewers:", labelWidth)} ${top}`);
	console.log(`  ${label("Since:", labelWidth)} ${sinceStr}`);
	console.log(`  ${label("Output:", labelWidth)} ${dim(outputPath)}`);
	console.log(`  ${label("Details:", labelWidth)} ${dim(detailsPath)}`);
	console.log(`  ${label("Analysis:", labelWidth)} ${dim(analysisPath)}`);
	console.log("");

	// ─── Step 1/3: Top Reviewers ────────────────────────────────────────────
	console.log(formatStep(1, 3, "Discovering Top Reviewers"));
	console.log("");

	spinner.start("Connecting to GitHub API...");
	const graphqlClient = createGraphQLClient();

	const hasAccess = await validateOrgAccess(graphqlClient, org);
	if (!hasAccess) {
		spinner.stop();
		throw new Error(`No access to organization: ${org}`);
	}

	const initialRateLimit = await checkRateLimit(graphqlClient);
	spinner.stopWithMessage(formatSuccess("Connected to GitHub API"));
	console.log(dim(`  Rate limit: ${initialRateLimit.remaining} points remaining`));

	spinner.start("Fetching repositories...");
	const repoNames = await fetchOrgRepos(graphqlClient, {
		org,
		filterRepos: repos.length > 0 ? repos : undefined,
		onProgress: (count) => {
			spinner.update(`Fetching repositories... ${dim(`(${count} found)`)}`);
		},
	});
	spinner.stopWithMessage(formatSuccess(`Found ${repoNames.length} repositories`));

	if (repoNames.length === 0) {
		console.log(dim("  No repositories found."));
		return;
	}

	spinner.start("Fetching review activity...");
	const allActivities: Map<string, UserActivity>[] = [];
	const allDetails: ReviewCommentDetail[][] = [];

	let repoIndex = 0;
	for (const repo of repoNames) {
		repoIndex++;
		spinner.update(
			`Fetching review activity... ${dim(`[${repoIndex}/${repoNames.length}] ${repo}`)}`,
		);

		try {
			const result = await fetchReviewActivity(graphqlClient, {
				org,
				repo,
				since,
			});
			if (result.activity.size > 0) allActivities.push(result.activity);
			if (result.details.length > 0) allDetails.push(result.details);
		} catch (error) {
			if (error instanceof Error && error.message.includes("Could not resolve")) {
				continue;
			}
			spinner.stop();
			throw error;
		}
	}
	spinner.stopWithMessage(formatSuccess(`Scanned ${repoNames.length} repositories`));

	console.log(dim("  Aggregating results..."));
	const aggregated = aggregateActivity(allActivities);
	const topReviewers = topN(aggregated, top);

	const step1Metadata = {
		org,
		since: sinceStr,
		analyzedAt: new Date().toISOString().slice(0, 10),
		totalRepos: repoNames.length,
		totalContributors: aggregated.length,
	};

	printTable(topReviewers, step1Metadata);

	await mkdir(dirname(detailsPath), { recursive: true });
	const topReviewerLogins = new Set(topReviewers.map((r) => r.login));
	const aggregatedDetails = aggregateDetails(allDetails);
	const filteredDetails = aggregatedDetails.filter((d) => topReviewerLogins.has(d.reviewer));
	await writeDetailsJSON(filteredDetails, detailsPath, step1Metadata);
	console.log(
		formatSuccess(
			`Details written to ${detailsPath} (${filteredDetails.length} comments from top ${top} reviewers)`,
		),
	);

	const prRankings = aggregatePRRankings(filteredDetails);
	const csvPath = derivePRRankingsCSVPath(detailsPath);
	await writePRRankingsCSV(prRankings, csvPath);
	console.log(formatSuccess(`PR rankings written to ${csvPath}`));
	console.log("");

	// ─── Step 2/3: Analyze ──────────────────────────────────────────────────
	console.log(formatStep(2, 3, "Analyzing Review Patterns"));
	console.log("");

	const { comments } = await parseInputJSON(detailsPath);
	console.log(dim(`  Parsed ${comments.length} comments`));

	let reviewerGroups = groupByReviewer(comments);

	if (maxComments) {
		reviewerGroups = limitCommentsPerReviewer(reviewerGroups, maxComments);
	}

	console.log(
		`  ${label("Reviewers:", labelWidth)} ${dim(reviewerGroups.map((g) => `${g.reviewer} (${g.totalComments})`).join(", "))}`,
	);

	spinner.start("Analyzing patterns with Claude AI...");
	const analyzeClient = createAnalyzeClient();

	const getMaxCommentsPerReviewer = (groups: typeof reviewerGroups) =>
		Math.max(...groups.map((g) => g.totalComments));

	const minComments = 1;
	let currentMaxComments = maxComments || getMaxCommentsPerReviewer(reviewerGroups);
	let currentLimit = currentMaxComments;
	let isRetrying = false;
	let analysis = "";

	while (true) {
		const groupsToAnalyze =
			currentLimit < getMaxCommentsPerReviewer(reviewerGroups)
				? limitCommentsPerReviewer(reviewerGroups, currentLimit)
				: reviewerGroups;

		const analysisPrompt = buildAnalysisPrompt(groupsToAnalyze);
		const estimatedTokens = estimateTokenCount(analysisPrompt);
		const totalComments = groupsToAnalyze.reduce((sum, g) => sum + g.totalComments, 0);

		if (isRetrying) {
			spinner.log(
				dim(
					`  Retrying with max ${currentLimit} comments/reviewer (${totalComments} total, ~${estimatedTokens.toLocaleString()} tokens)`,
				),
			);
		} else {
			spinner.log(
				dim(`  Sending ${totalComments} comments (~${estimatedTokens.toLocaleString()} tokens)`),
			);
		}

		try {
			analysis = await analyzeReviews(analyzeClient, groupsToAnalyze, buildAnalysisPrompt, {
				model: analyzeModel,
			});
			break;
		} catch (error) {
			if (isTokenLimitError(error)) {
				isRetrying = true;
				currentMaxComments = currentLimit;
				currentLimit = Math.floor((minComments + currentMaxComments) / 2);
				if (currentLimit < minComments || currentLimit === currentMaxComments) {
					spinner.stop();
					throw error;
				}
				spinner.log(
					yellow(`  Token limit exceeded, reducing to ${currentLimit} comments per reviewer...`),
				);
				continue;
			}
			spinner.stop();
			throw error;
		}
	}

	spinner.stopWithMessage(formatSuccess("Analysis complete"));

	const step2Metadata = {
		inputFile: detailsPath,
		totalComments: comments.length,
		reviewers: reviewerGroups.map((g) => g.reviewer),
		analyzedAt: new Date().toISOString(),
	};

	await mkdir(dirname(analysisPath), { recursive: true });
	await Bun.write(analysisPath, formatMarkdownReport(analysis, step2Metadata));
	console.log(formatSuccess(`Analysis written to ${analysisPath}`));
	console.log("");

	// ─── Step 3/3: Generate Prompt ──────────────────────────────────────────
	console.log(formatStep(3, 3, "Generating PR Review Prompt"));
	console.log("");

	const analysisContent = await Bun.file(analysisPath).text();

	console.log(dim(`  Model: ${promptModel}`));
	spinner.start("Generating PR review prompt with Claude AI...");
	const promptClientInstance = createPromptClient();
	try {
		const generatedPrompt = await generateReviewPrompt(promptClientInstance, analysisContent, {
			model: promptModel,
			includeReviewerAttribution: includeAttribution,
		});
		spinner.stopWithMessage(formatSuccess("Prompt generated"));

		await mkdir(dirname(outputPath), { recursive: true });
		const footer = `\n\n---\n\n*Generated: ${new Date().toISOString()}*\n*Source: ${detailsPath}*\n*Model: ${promptModel}*`;
		await Bun.write(outputPath, generatedPrompt + footer);
	} catch (error) {
		spinner.stop();
		throw error;
	}

	console.log(formatSuccess(`Prompt written to ${outputPath}`));

	printSuccess("Pipeline complete");
}
