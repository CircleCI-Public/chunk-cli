import type { ReviewCommentDetail, UserActivity } from "../types";

// Merge multiple repo activity maps into a single aggregated list
export function aggregateActivity(repoActivities: Map<string, UserActivity>[]): UserActivity[] {
	const merged = new Map<string, UserActivity>();

	for (const repoActivity of repoActivities) {
		for (const [login, activity] of repoActivity) {
			const existing = merged.get(login);

			if (existing) {
				// Sum up counts
				existing.totalActivity += activity.totalActivity;
				existing.reviewsGiven += activity.reviewsGiven;
				existing.approvals += activity.approvals;
				existing.changesRequested += activity.changesRequested;
				existing.reviewComments += activity.reviewComments;

				// Merge repo sets
				for (const repo of activity.reposActiveIn) {
					existing.reposActiveIn.add(repo);
				}
			} else {
				// Clone the activity (to avoid mutating original)
				merged.set(login, {
					login: activity.login,
					totalActivity: activity.totalActivity,
					reviewsGiven: activity.reviewsGiven,
					approvals: activity.approvals,
					changesRequested: activity.changesRequested,
					reviewComments: activity.reviewComments,
					reposActiveIn: new Set(activity.reposActiveIn),
				});
			}
		}
	}

	// Convert to sorted array
	return Array.from(merged.values()).sort((a, b) => b.totalActivity - a.totalActivity);
}

// Get top N contributors
export function topN(activities: UserActivity[], n: number): UserActivity[] {
	return activities.slice(0, n);
}

// Aggregate comment details from multiple repos
export function aggregateDetails(allDetails: ReviewCommentDetail[][]): ReviewCommentDetail[] {
	return allDetails.flat();
}
