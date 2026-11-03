import type { GitPollInputBody, GitPollView } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { relativeTime } from "#/lib/format";
import { shortSha } from "./model";

type PollInterval = NonNullable<GitPollInputBody["intervalMinutes"]>;

/** What the interval select holds: "off", or the minutes between two checks. */
export type PollChoice = "off" | `${PollInterval}`;

export interface PollOption {
	value: PollChoice;
	/** Plain text for the trigger and the list. */
	label: string;
}

export const POLL_OPTIONS: readonly PollOption[] = [
	{ value: "off", label: "Off" },
	{ value: "1", label: "Every minute" },
	{ value: "5", label: "Every 5 minutes" },
	{ value: "15", label: "Every 15 minutes" },
	{ value: "60", label: "Every hour" },
];

/** The interval the server uses when none is sent. */
const DEFAULT_POLL_INTERVAL: PollInterval = 5;

/** Only an app or worker built from a git source has a branch to check. */
export function canPollGit(
	service: Pick<Service, "kind" | "sourceType">,
): boolean {
	return (
		(service.kind === "app" || service.kind === "worker") &&
		service.sourceType === "git"
	);
}

/** The select's value for a service's poll settings; null (no row) is off. */
export function pollChoice(poll: GitPollView | null): PollChoice {
	if (!poll?.enabled) return "off";
	return `${poll.intervalMinutes}`;
}

/** Whether a commit found by checking is deployed: on unless it was switched off. */
export function pollDeploys(poll: GitPollView | null): boolean {
	return poll?.deploy ?? true;
}

/**
 * The full body of a PUT: the endpoint replaces the whole setting, so every
 * field is sent. Turning checking off keeps the interval last used, so turning
 * it on again starts from it.
 */
export function pollInput(
	choice: PollChoice,
	deploy: boolean,
	previous: GitPollView | null,
): GitPollInputBody {
	if (choice === "off") {
		return {
			enabled: false,
			intervalMinutes: previous?.intervalMinutes ?? DEFAULT_POLL_INTERVAL,
			deploy,
		};
	}
	return {
		enabled: true,
		intervalMinutes: Number(choice) as PollInterval,
		deploy,
	};
}

export interface PollStatus {
	tone: "muted" | "update" | "error";
	/** One line: when it was last checked and where the branch is, the new commit, or the error. */
	text: string;
	/** The commit the branch is at now, when there is one to deploy. */
	headSha?: string;
}

/**
 * The status line under the interval. `runningSha` is the commit the current
 * release was built from. Null when there is nothing to say yet (checking is
 * off and nobody asked).
 */
export function pollStatus(
	poll: GitPollView | null,
	runningSha: string | undefined,
): PollStatus | null {
	if (!poll) return null;
	if (poll.error) return { tone: "error", text: poll.error };
	const branch = poll.headBranch || "the branch";
	if (poll.updateAvailable && poll.headSha) {
		return {
			tone: "update",
			text: `New commit ${shortSha(poll.headSha)} on ${branch}`,
			headSha: poll.headSha,
		};
	}
	const when = relativeTime(poll.checkedAt);
	if (!when) {
		return poll.enabled ? { tone: "muted", text: "Not checked yet" } : null;
	}
	const head = poll.headSha
		? ` · ${branch} is at ${shortSha(poll.headSha)}${
				runningSha && runningSha === poll.headSha ? " (running)" : ""
			}`
		: "";
	return { tone: "muted", text: `Last checked ${when}${head}` };
}

/** The card's hint for a commit waiting to be deployed, or null. */
export function newCommitHint(
	poll: GitPollView | null | undefined,
): string | null {
	if (!poll?.updateAvailable || !poll.headSha) return null;
	return `New commit ${shortSha(poll.headSha)}`;
}
