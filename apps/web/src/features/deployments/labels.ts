import type { Deployment, DeploymentStatus, ReleaseSource } from "#/api/models";
import type { StatusKind } from "#/components/ui/status-icon";
import { commitSubject } from "#/features/git/model";

const IN_FLIGHT: Record<DeploymentStatus, boolean> = {
	created: true,
	queued: true,
	preparing: true,
	building: true,
	releasing: true,
	verifying: true,
	succeeded: false,
	failed: false,
	canceled: false,
	rolled_back: false,
	rollback_failed: false,
};

/** The deployment has not reached a terminal status: it can still change. */
export function isInFlight(status: DeploymentStatus): boolean {
	return IN_FLIGHT[status];
}

export const STATUS_LABEL: Record<DeploymentStatus, string> = {
	created: "Queued",
	queued: "Queued",
	preparing: "Preparing",
	building: "Building",
	releasing: "Releasing",
	verifying: "Verifying",
	succeeded: "Succeeded",
	failed: "Failed",
	canceled: "Canceled",
	rolled_back: "Rolled back",
	rollback_failed: "Rollback failed",
};

/** The status icon of a deployment's status; the label always accompanies it. */
export const STATUS_KIND: Record<DeploymentStatus, StatusKind> = {
	created: "pending",
	queued: "pending",
	preparing: "building",
	building: "building",
	releasing: "deploying",
	verifying: "verifying",
	succeeded: "live",
	failed: "failed",
	canceled: "stopped",
	rolled_back: "degraded",
	rollback_failed: "failed",
};

/**
 * The status icon of one deployment row. A succeeded deployment carries its
 * icon only while it is the one serving; once superseded the word "Succeeded"
 * stands alone (`null`).
 */
export function deploymentStatusKind(
	status: DeploymentStatus,
	live: boolean,
): StatusKind | null {
	if (status === "succeeded" && !live) return null;
	return STATUS_KIND[status];
}

const CANCELABLE: ReadonlySet<DeploymentStatus> = new Set([
	"created",
	"queued",
	"preparing",
	"building",
	"releasing",
	"verifying",
]);

/**
 * A deployment can be canceled at any step until it is live: while it is
 * created, queued, preparing, building, releasing or verifying. Before the
 * agent started it, it ends as canceled at once; after, the agent aborts and
 * unwinds, and the control plane has the last word (409 `cannot_cancel`).
 */
export function isCancelable(status: DeploymentStatus): boolean {
	return CANCELABLE.has(status);
}

/** Agent steps whose name is not just the capitalised step id. */
const STEP_LABEL: Record<string, string> = {
	predeploy: "Pre-deploy",
	postdeploy: "Post-deploy",
	push: "Push to registry",
};

/** A timeline step as it reads: `predeploy` is "Pre-deploy", `postdeploy` "Post-deploy", other ids stay as the agent sends them. */
export function stepLabel(step: string): string {
	return STEP_LABEL[step.toLowerCase()] ?? step;
}

/** "Failed at Verify": where a failed deployment stopped, or null when unknown. */
export function failedAtLabel(step: string | null | undefined): string | null {
	const name = step?.trim();
	if (!name) return null;
	const label =
		STEP_LABEL[name.toLowerCase()] ??
		`${name.charAt(0).toUpperCase()}${name.slice(1)}`;
	return `Failed at ${label}`;
}

/** Statuses whose `statusMessage` explains why the deployment did not land. */
const FAILURE_STATUS: Record<DeploymentStatus, boolean> = {
	created: false,
	queued: false,
	preparing: false,
	building: false,
	releasing: false,
	verifying: false,
	succeeded: false,
	failed: true,
	canceled: true,
	rolled_back: true,
	rollback_failed: true,
};

/** `a91c3f2 · main`: what a git release is when no commit message exists. */
function gitSummary(source: ReleaseSource): string {
	const sha = (source.commitSha ?? "").slice(0, 7);
	return [sha, source.branch].filter(Boolean).join(" · ");
}

export interface DeploymentMessage {
	/** The line the deployment is known by. */
	text: string;
	/** `note` is written by a person; the rest are derived. */
	kind: "note" | "failure" | "release" | "trigger";
	/** The release identity (digest or sha) when `text` does not already show it. */
	detail: string | null;
}

/**
 * The message of a deployment row, never the agent's result text (that names
 * containers). In order: the note it was deployed with; for a failure, why it
 * failed ("Failed at Verify: …"); the release itself (a git commit's sha and
 * branch, or the image reference); finally what triggered it.
 */
export function deploymentMessage(
	deployment: Pick<
		Deployment,
		"message" | "statusMessage" | "trigger" | "status" | "release"
	> & { commit?: Deployment["commit"] },
	failedStep?: string | null,
): DeploymentMessage {
	const source = deployment.release?.source;
	const identity = deployment.release
		? releaseLabel(source, deployment.release.imageRef)
		: "";
	const withDetail = (
		text: string,
		kind: DeploymentMessage["kind"],
	): DeploymentMessage => ({
		text,
		kind,
		detail: identity && !text.includes(identity) ? identity : null,
	});

	const note = deployment.message?.trim();
	if (note) return withDetail(note, "note");

	const reason = deployment.statusMessage?.trim();
	if (reason && FAILURE_STATUS[deployment.status]) {
		const at = failedAtLabel(failedStep);
		return withDetail(at ? `${at}: ${reason}` : reason, "failure");
	}

	// A push or pull request deployment is known by its commit message.
	const subject = deployment.commit?.message
		? commitSubject(deployment.commit.message)
		: "";
	if (subject) {
		const by = deployment.commit?.author
			? ` · ${deployment.commit.author}`
			: "";
		return withDetail(`${subject}${by}`, "release");
	}
	if (source?.commitSha) {
		const text = gitSummary(source);
		if (text) return withDetail(text, "release");
	}
	const image = source?.image || deployment.release?.imageRef;
	if (image) return withDetail(image, "release");

	return {
		text: TRIGGER_LABEL[deployment.trigger],
		kind: "trigger",
		detail: null,
	};
}

export const TRIGGER_LABEL: Record<Deployment["trigger"], string> = {
	manual: "Deploy",
	redeploy: "Redeploy",
	rollback: "Rollback",
	staged: "Staged changes",
	reconcile: "Auto-recovery",
	drain: "Server drain",
	git_push: "Git push",
	preview: "Pull-request preview",
	cron: "Scheduled task",
	deploy_hook: "Via deploy hook",
};

/**
 * What a release is, in a few characters: `abc1234` for a git commit,
 * `sha256:0a1b2c3d` for an image digest, or the image reference itself.
 */
export function releaseLabel(
	source: ReleaseSource | undefined,
	imageRef = "",
): string {
	if (!source) return imageRef || "unknown release";
	if (source.commitSha) return source.commitSha.slice(0, 7);
	const digest = source.digest ?? "";
	const match = /sha256:([0-9a-f]{8})/.exec(digest);
	if (match) return `sha256:${match[1]}`;
	return source.image ?? imageRef ?? "unknown release";
}

/** The source line under a deployment: "repo@sha256:…" or "git url @ sha7". */
export function releaseDetail(
	source: ReleaseSource | undefined,
	imageRef = "",
): string {
	if (!source) return imageRef;
	if (source.commitSha) {
		const repo = (source.gitUrl ?? "")
			.replace(/^https?:\/\//, "")
			.replace(/\.git$/, "");
		return `${repo} @ ${source.commitSha.slice(0, 7)}`;
	}
	return source.digest ?? source.image ?? imageRef;
}

/** Wall-clock span of a deployment, or null while it has not started. */
export function durationMs(
	deployment: Deployment,
	now = Date.now(),
): number | null {
	const started = Date.parse(deployment.startedAt ?? deployment.createdAt);
	if (Number.isNaN(started)) return null;
	const finished = deployment.finishedAt
		? Date.parse(deployment.finishedAt)
		: now;
	return Math.max(0, finished - started);
}
