import type { TeamSliceView } from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";
import { sizeText, usedPercent, WARN_PERCENT } from "./model";

// What a team sees of the built-in services: used space and quota of its backup
// storage, its state and nothing else. Never a key, bucket or address: the
// control plane does not send them. Pure, so it is tested.

export type BackupSlice = TeamSliceView["backup"];
export type BackupSliceState = BackupSlice["state"];

export interface BackupStateWords {
	kind: StatusKind;
	label: string;
	/** What it means for the team, one line; empty while all is well. */
	detail: string;
}

const STATE: Record<BackupSliceState, BackupStateWords> = {
	ready: { kind: "live", label: "Ready", detail: "" },
	provisioning: {
		kind: "deploying",
		label: "Being set up",
		detail: "Backups to it work as soon as it is ready.",
	},
	full: {
		kind: "warning",
		label: "Full",
		detail:
			"New backups fail until space is freed or your instance admin raises the quota. Existing backups are kept.",
	},
	failed: {
		kind: "failed",
		label: "Not working",
		detail: "Ask your instance admin to look at it. Existing backups are kept.",
	},
	unavailable: {
		kind: "offline",
		label: "Not available",
		detail: "This installation has no built-in backup storage.",
	},
};

export function backupStateWords(state: BackupSliceState): BackupStateWords {
	return STATE[state];
}

/** The built-in storage exists for this team: anything but `unavailable`. */
export function hasBuiltinStorage(slice: BackupSlice | undefined): boolean {
	return slice !== undefined && slice.state !== "unavailable";
}

export interface BackupUsage {
	/** "12.4 GiB of 50 GiB". */
	text: string;
	percent: number;
	/** The bar value, held at 100 when the quota was lowered under the use. */
	barValue: number;
	/** At the point the team is alerted: the bar turns amber. */
	warn: boolean;
	/** The 80% / 100% sentence, or null while it fits. */
	note: string | null;
}

/** Used over quota, for the card: the sentence, the bar and the warning. */
export function backupUsage(slice: BackupSlice): BackupUsage {
	const percent = usedPercent(slice.usedBytes, slice.quotaBytes);
	let note: string | null = null;
	if (slice.state !== "full" && percent >= 100) {
		note = "The quota is reached: new backups fail until space is freed.";
	} else if (percent >= WARN_PERCENT && slice.state !== "full") {
		note = "Nearly full: you are alerted at 80% and at 100%.";
	}
	return {
		text: `${sizeText(slice.usedBytes)} of ${sizeText(slice.quotaBytes)}`,
		percent,
		barValue: Math.min(percent, 100),
		warn: percent >= WARN_PERCENT,
		note,
	};
}
