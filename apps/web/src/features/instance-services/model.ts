import { ApiError } from "#/api/client-config";
import type {
	InstanceServicesView,
	InstanceServiceView,
	SliceView,
} from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";

// Pure helpers for Admin → Built-in services: byte and GiB conversion, the
// quota rules the control plane enforces, one row per team from the slices,
// and the words for each state. Nothing here touches React.

export const GIB = 1024 ** 3;

/** The smallest quota the control plane accepts, in GiB. */
export const MIN_QUOTA_GIB = 1;

/** The largest value the field takes: far above any disk, and exact in a JS number. */
export const MAX_QUOTA_GIB = 1_000_000;

export type ServiceKind = InstanceServiceView["kind"];
export type ServiceStatus = InstanceServiceView["status"];
export type SliceState = SliceView["state"];

/**
 * What the page says when the services did not load. A status means the control
 * plane answered (an older build, a refusal), and the line says what it
 * answered; only a call that got no answer says that it did not.
 */
export function servicesLoadFailure(error: unknown): string {
	if (error instanceof ApiError) {
		const reason = error.message.replace(/\.+$/, "");
		return `The control plane answered ${error.status}: ${reason}. Nothing was changed.`;
	}
	return "The control plane did not answer, so nothing was changed.";
}

/** What each service is called on the page. The kinds are not product names. */
export const SERVICE_LABEL: Record<ServiceKind, string> = {
	s3: "Backup storage",
	registry: "Image registry",
	loki: "Central logs",
};

/** What each service holds, in one line. */
export const SERVICE_PURPOSE: Record<ServiceKind, string> = {
	s3: "NebulaCtrl's own data for each team: its backups. A project that needs S3 runs its own.",
	registry:
		"The images built for each team, kept per team and pruned when unused.",
	loki: "Each team's logs, kept for 7 days, for teams that turn forwarding on.",
};

/** The order the services are listed in. */
export const SERVICE_ORDER: readonly ServiceKind[] = ["s3", "registry", "loki"];

export function bytesToGib(bytes: number): number {
	return bytes / GIB;
}

/** "50" for 50 GiB, "1.5" for 1.5 GiB: what the quota field shows. */
export function gibText(bytes: number): string {
	const gib = bytesToGib(bytes);
	return String(Math.round(gib * 100) / 100);
}

/** "50 GiB", "512 MiB": a quota or a use, with the binary unit. */
export function sizeText(bytes: number): string {
	if (bytes <= 0) return "0 B";
	const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
	const exponent = Math.min(
		Math.floor(Math.log(bytes) / Math.log(1024)),
		units.length - 1,
	);
	const value = bytes / 1024 ** exponent;
	const rounded = Math.round(value * 10) / 10;
	const decimals = exponent > 0 && !Number.isInteger(rounded) ? 1 : 0;
	return `${rounded.toFixed(decimals)} ${units[exponent]}`;
}

export type QuotaParse =
	| { ok: true; bytes: number }
	| { ok: false; message: string };

/**
 * The text of a quota field in GiB, as bytes. Whole or decimal numbers; the
 * minimum is 1 GiB, the same rule the control plane answers with `quota_too_small`.
 */
export function parseQuotaGib(text: string): QuotaParse {
	const value = text.trim();
	if (value === "") return { ok: false, message: "Enter a size in GiB." };
	if (!/^\d+(\.\d{1,2})?$/.test(value)) {
		return { ok: false, message: "Enter a number of GiB, like 50 or 12.5." };
	}
	const gib = Number(value);
	if (gib < MIN_QUOTA_GIB) {
		return { ok: false, message: "The smallest quota is 1 GiB." };
	}
	if (gib > MAX_QUOTA_GIB) {
		return { ok: false, message: "That is more than any disk holds." };
	}
	return { ok: true, bytes: Math.round(gib * GIB) };
}

/** Used as a whole percentage of the quota; above 100 when the quota was lowered under the use. */
export function usedPercent(usedBytes: number, quotaBytes: number): number {
	if (quotaBytes <= 0) return 0;
	return Math.floor((usedBytes * 100) / quotaBytes);
}

/** At and above this, the bar turns amber: the same point the team is alerted at. */
export const WARN_PERCENT = 80;

// --- States, in words --------------------------------------------------------

const STATUS_KIND: Record<ServiceStatus, StatusKind> = {
	ready: "live",
	provisioning: "deploying",
	failed: "failed",
};

const STATUS_LABEL: Record<ServiceStatus, string> = {
	ready: "Ready",
	provisioning: "Starting",
	failed: "Not working",
};

export function serviceStatus(status: ServiceStatus): {
	kind: StatusKind;
	label: string;
} {
	return { kind: STATUS_KIND[status], label: STATUS_LABEL[status] };
}

const SLICE_KIND: Record<SliceState, StatusKind> = {
	ready: "live",
	provisioning: "deploying",
	full: "warning",
	failed: "failed",
};

const SLICE_LABEL: Record<SliceState, string> = {
	ready: "Ready",
	provisioning: "Being set up",
	full: "Full",
	failed: "Not working",
};

export function sliceStatus(state: SliceState): {
	kind: StatusKind;
	label: string;
} {
	return { kind: SLICE_KIND[state], label: SLICE_LABEL[state] };
}

/** Worst first: what a team's row shows when its slices disagree. */
const SLICE_SEVERITY: Record<SliceState, number> = {
	failed: 3,
	full: 2,
	provisioning: 1,
	ready: 0,
};

// --- One row per team --------------------------------------------------------

export interface TeamRow {
	teamId: string;
	teamName: string;
	/** The backup storage slice: the one with a quota. Absent until it is created. */
	backup: SliceView | undefined;
	/** The worst state among the team's slices. */
	state: SliceState;
	/** Why the first failed slice is not working, with the service named: "Central logs: tunnel closed". */
	message: string;
	/** The team was deleted: its slices are kept until `purgeAt`. */
	deleted: boolean;
	deletedAt: string | null;
	purgeAt: string | null;
}

function earliest(
	values: readonly (string | null | undefined)[],
): string | null {
	let best: string | null = null;
	for (const value of values) {
		if (value && (best === null || value < best)) best = value;
	}
	return best;
}

/**
 * The slices of every team as one row per team: live teams by name, then the
 * deleted ones by the day they are purged. The row's quota, use and bar come
 * from the backup storage slice; the state is the worst of all its slices.
 */
export function teamRows(slices: readonly SliceView[]): TeamRow[] {
	const byTeam = new Map<string, SliceView[]>();
	for (const slice of slices) {
		const list = byTeam.get(slice.teamId);
		if (list) list.push(slice);
		else byTeam.set(slice.teamId, [slice]);
	}
	const rows: TeamRow[] = [];
	for (const [teamId, list] of byTeam) {
		let state: SliceState = "ready";
		let message = "";
		for (const slice of list) {
			if (SLICE_SEVERITY[slice.state] > SLICE_SEVERITY[state])
				state = slice.state;
			if (message === "" && slice.state === "failed") {
				// A team's row shows its worst slice, so the line names which one.
				message = `${SERVICE_LABEL[slice.kind]}: ${slice.message || "not working"}`;
			}
		}
		const deletedAt = earliest(list.map((slice) => slice.deletedAt));
		rows.push({
			teamId,
			teamName: list[0]?.teamName ?? teamId,
			backup: list.find((slice) => slice.kind === "s3"),
			state,
			message,
			deleted: deletedAt !== null,
			deletedAt,
			purgeAt: earliest(list.map((slice) => slice.purgeAt)),
		});
	}
	return rows.sort((a, b) => {
		if (a.deleted !== b.deleted) return a.deleted ? 1 : -1;
		if (a.deleted && b.deleted) {
			return (a.purgeAt ?? "").localeCompare(b.purgeAt ?? "");
		}
		return a.teamName.localeCompare(b.teamName);
	});
}

/** The quota line under a team's name: its own, or the instance default. */
export function quotaSource(slice: SliceView | undefined): string {
	if (!slice) return "";
	return slice.quotaBytes === null || slice.quotaBytes === undefined
		? "Default"
		: "Set for this team";
}

/** Days left before a deleted team's slices are purged ("in 12 days", "today"). */
export function purgeIn(purgeAt: string | null, now: number): string {
	if (!purgeAt) return "";
	const ms = new Date(purgeAt).getTime() - now;
	if (ms <= 0) return "today";
	const days = Math.ceil(ms / 86_400_000);
	return days === 1 ? "in 1 day" : `in ${days} days`;
}

/** Quota edit is possible for a live team that has its backup slice. */
export function canEditQuota(row: TeamRow): boolean {
	return !row.deleted && row.backup !== undefined;
}

/** What the table's footer line says: the totals across live teams. */
export function totals(view: InstanceServicesView): {
	teams: number;
	deleted: number;
	usedBytes: number;
} {
	const rows = teamRows(view.slices ?? []);
	const usedBytes = rows.reduce(
		(sum, row) => sum + (row.backup?.usedBytes ?? 0),
		0,
	);
	return {
		teams: rows.filter((row) => !row.deleted).length,
		deleted: rows.filter((row) => row.deleted).length,
		usedBytes,
	};
}

/** The 80% warning line of a team row, or null while it fits. */
export function usageNote(row: TeamRow): string | null {
	const slice = row.backup;
	if (!slice || row.deleted) return null;
	const percent = usedPercent(slice.usedBytes, slice.effectiveQuotaBytes);
	if (slice.state === "full" || percent >= 100) {
		return "New backups fail until space is freed or the quota is raised.";
	}
	if (percent >= WARN_PERCENT) {
		return "Nearly full: the team was warned at 80%.";
	}
	return null;
}
