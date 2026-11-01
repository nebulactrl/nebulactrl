import type {
	DatabaseView,
	RotationPlan,
	UpgradePlan,
} from "#/api/gen/types.gen";
import type { Service } from "#/api/models";

/** How long a refresh may stay unanswered before the UI calls the numbers stale. */
export const REFRESH_TIMEOUT_MS = 20_000;

export interface RefreshRequest {
	/** `Date.now()` when the refresh was requested. */
	at: number;
	/** `info.updatedAt` as it was then (undefined when there was no sample yet). */
	baseline: string | undefined;
}

export type RefreshStatus = "idle" | "pending" | "unanswered";

/**
 * A refresh is a 202: the numbers arrive later in `info`. It is answered once
 * `info.updatedAt` differs from what it was at request time (no clock
 * comparison between browser and server), pending until then, and
 * unanswered after the timeout.
 */
export function refreshStatus(
	request: RefreshRequest | null,
	updatedAt: string | undefined,
	now: number,
): RefreshStatus {
	if (request === null || updatedAt !== request.baseline) return "idle";
	return now - request.at < REFRESH_TIMEOUT_MS ? "pending" : "unanswered";
}

/** Why the numbers cannot be refreshed right now, or null when they can. */
export function refreshBlocker(
	service: Pick<Service, "desiredState">,
	database: Pick<DatabaseView, "deployed">,
): string | null {
	if (!database.deployed) {
		return "Not deployed yet: deploy it to read its size and connections.";
	}
	if (service.desiredState === "stopped") {
		return "Stopped: start it to read its size and connections.";
	}
	return null;
}

/** Polling cadence for the database view: fast while the agent is working, off otherwise. */
export function databasePollInterval(
	database: Pick<DatabaseView, "rotation" | "upgrade"> | undefined,
	refreshing: boolean,
	pollMs: number,
): number | false {
	if (
		refreshing ||
		database?.rotation?.status === "pending" ||
		database?.upgrade?.status === "pending"
	) {
		return pollMs;
	}
	return false;
}

/** The primary export (the one Copy connection string reveals), if any. */
export function primaryExport(
	database: Pick<DatabaseView, "exports">,
): NonNullable<DatabaseView["exports"]>[number] | undefined {
	const exports = database.exports ?? [];
	return exports.find((entry) => entry.primary) ?? exports[0];
}

export const ROTATION_CONFIRM: Record<RotationPlan["mode"], string> = {
	exec: "Rotate password now",
	redeploy: "Stage new password",
	variable: "Stage new password",
};

/** What each rotation mode does, in a sentence. */
export const ROTATION_MODE_TEXT: Record<RotationPlan["mode"], string> = {
	exec: "The password is changed inside the running database. The old password stops working immediately.",
	redeploy:
		"A new password is staged. The database picks it up when it is deployed again.",
	variable:
		"A new password is staged in the variable. It takes effect on the next deploy.",
};

/** The rotate button is locked by blockers, or until a plan has loaded. */
export function canRotate(plan: RotationPlan | undefined): boolean {
	return plan !== undefined && (plan.blockers ?? []).length === 0;
}

/**
 * The upgrade confirm needs an executable plan without blockers and the
 * service's slug typed exactly, as for deleting a service.
 */
export function canConfirmUpgrade(
	plan: UpgradePlan | undefined,
	typed: string,
	slug: string,
): boolean {
	return (
		plan?.executable === true &&
		plan.kind === "upgrade" &&
		(plan.blockers ?? []).length === 0 &&
		typed === slug
	);
}

export const UPGRADE_METHOD_LABEL: Record<string, string> = {
	none: "No change",
	redeploy: "Redeploy with the new image",
	pg_upgrade: "pg_upgrade into a new volume",
	dump_restore: "Dump and restore (manual)",
};

export type UpgradeBanner =
	| { tone: "pending"; text: string }
	| { tone: "ready"; text: string }
	| { tone: "failed"; text: string }
	| { tone: "done"; text: string };

/**
 * What to say about the latest upgrade. `ready` means the data is converted
 * and a staged change waits for a deploy; once that deploy ran (no staged
 * change left) it reads as done.
 */
export function upgradeBanner(
	upgrade: DatabaseView["upgrade"],
	engineLabel: string,
	hasStagedChanges: boolean,
): UpgradeBanner | null {
	if (!upgrade) return null;
	const target = `${engineLabel} ${upgrade.toVersion}`;
	switch (upgrade.status) {
		case "pending":
			return {
				tone: "pending",
				text: `Upgrading ${engineLabel} ${upgrade.fromVersion} to ${upgrade.toVersion}. The database is stopped while its data is converted.`,
			};
		case "ready":
			return hasStagedChanges
				? {
						tone: "ready",
						text: `Deploy to start ${target}; the previous volume “${upgrade.oldVolume}” is kept.`,
					}
				: {
						tone: "done",
						text: `Upgraded from ${upgrade.fromVersion} to ${upgrade.toVersion}. The previous volume “${upgrade.oldVolume}” is kept.`,
					};
		case "failed":
			return {
				tone: "failed",
				text: upgrade.error
					? `The upgrade to ${upgrade.toVersion} failed: ${upgrade.error}`
					: `The upgrade to ${upgrade.toVersion} failed.`,
			};
	}
}

/** The latest rotation as a line of text, or null when there is nothing to say. */
export function rotationLine(
	rotation: DatabaseView["rotation"],
): { tone: "pending" | "failed" | "applied"; text: string } | null {
	if (!rotation) return null;
	switch (rotation.status) {
		case "pending":
			return {
				tone: "pending",
				text: "Rotating credentials: the agent is changing the password.",
			};
		case "failed":
			return {
				tone: "failed",
				text: rotation.error
					? `The last rotation failed: ${rotation.error}`
					: "The last rotation failed.",
			};
		case "applied":
			return { tone: "applied", text: "Credentials were rotated." };
	}
}
