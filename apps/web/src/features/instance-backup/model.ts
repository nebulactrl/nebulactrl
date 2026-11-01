import type {
	InstanceBackupView,
	PatchInstanceBackupInputBody,
} from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";
import { describeCron } from "#/features/backups/model";

/** The default schedule of the control-plane backup: daily at 03:30 UTC. */
export const DEFAULT_SCHEDULE = "30 3 * * *";

/** Retention bounds the API accepts. */
export const RETENTION_MIN = 1;
export const RETENTION_MAX = 365;

const pad = (n: number) => String(n).padStart(2, "0");

/** "HH:MM" (24 hour) → a daily cron, or undefined when the text is not a time. */
export function dailyCron(time: string): string | undefined {
	const m = /^(\d{1,2}):(\d{2})$/.exec(time.trim());
	if (!m) return undefined;
	const hour = Number(m[1]);
	const minute = Number(m[2]);
	if (hour > 23 || minute > 59) return undefined;
	return `${minute} ${hour} * * *`;
}

/** The "HH:MM" of a daily cron (`M H * * *`), or undefined for any other expression. */
export function dailyTime(cron: string): string | undefined {
	const m = /^(\d{1,2}) (\d{1,2}) \* \* \*$/.exec(cron.trim());
	if (!m) return undefined;
	const minute = Number(m[1]);
	const hour = Number(m[2]);
	if (hour > 23 || minute > 59) return undefined;
	return `${pad(hour)}:${pad(minute)}`;
}

/** "Every day at 03:30 UTC". Schedules are always evaluated in UTC. */
export function scheduleSummary(cron: string): string {
	return `${describeCron(cron)} UTC`;
}

/** "Keeps the newest 14 backups." */
export function retentionSummary(count: number): string {
	return `Keeps the newest ${count} backup${count === 1 ? "" : "s"}.`;
}

export interface RunPresentation {
	kind: StatusKind;
	label: string;
}

/** How a run is shown: the status icon and its words. */
export function runPresentation(status: string): RunPresentation {
	switch (status) {
		case "succeeded":
			return { kind: "healthy", label: "Succeeded" };
		case "failed":
			return { kind: "failed", label: "Failed" };
		case "running":
			return { kind: "building", label: "Running" };
		default:
			return { kind: "unknown", label: status };
	}
}

export const TRIGGER_LABEL: Record<string, string> = {
	schedule: "Scheduled",
	manual: "Manual",
	pre_upgrade: "Before upgrade",
};

export type DestKind = "s3" | "local" | "builtin";

/** The destination form. Keys are write-only: they start empty on an edit. */
export interface DestinationDraft {
	kind: DestKind;
	endpoint: string;
	region: string;
	bucket: string;
	prefix: string;
	pathStyle: boolean;
	compatMode: boolean;
	accessKey: string;
	secretKey: string;
	localDir: string;
}

export function draftFor(view: InstanceBackupView): DestinationDraft {
	const d = view.destination;
	return {
		kind: d.kind,
		endpoint: d.endpoint ?? "",
		region: d.region ?? "",
		bucket: d.bucket ?? "",
		prefix: d.prefix ?? "",
		pathStyle: d.pathStyle,
		compatMode: d.compatMode,
		accessKey: "",
		secretKey: "",
		localDir: d.localDir ?? "",
	};
}

/**
 * Whether the draft can be saved. An S3 destination needs a bucket and keys
 * (the stored keys count when the kind is unchanged and the form leaves them empty).
 */
export function draftValid(
	draft: DestinationDraft,
	current: InstanceBackupView,
): boolean {
	if (draft.kind === "local") return current.localDirEnabled;
	// The built-in storage needs nothing typed; the control plane says when it is not available.
	if (draft.kind === "builtin") return true;
	if (draft.bucket.trim() === "") return false;
	const keysStored =
		current.destination.kind === "s3" &&
		current.destination.accessKeySet &&
		current.destination.secretKeySet;
	const keysTyped = draft.accessKey !== "" && draft.secretKey !== "";
	return (
		keysTyped ||
		(keysStored && draft.accessKey === "" && draft.secretKey === "")
	);
}

/** The PATCH body for a destination edit. Empty keys are left out so the stored ones stay. */
export function draftToPatch(
	draft: DestinationDraft,
): PatchInstanceBackupInputBody {
	if (draft.kind === "builtin") {
		return { kind: "builtin", prefix: draft.prefix.trim() };
	}
	if (draft.kind === "local") {
		return {
			kind: "local",
			localDir: draft.localDir.trim(),
			prefix: draft.prefix.trim(),
		};
	}
	return {
		kind: "s3",
		endpoint: draft.endpoint.trim(),
		region: draft.region.trim(),
		bucket: draft.bucket.trim(),
		prefix: draft.prefix.trim(),
		pathStyle: draft.pathStyle,
		compatMode: draft.compatMode,
		...(draft.accessKey !== "" ? { accessKey: draft.accessKey } : {}),
		...(draft.secretKey !== "" ? { secretKey: draft.secretKey } : {}),
	};
}

/** True once an admin confirmed the instance secret key is stored elsewhere. */
export function keyAcknowledged(view: InstanceBackupView): boolean {
	return view.keyAcknowledgedAt !== undefined;
}

/** The one sentence that says a destination on this host is not off-site. */
export const NOT_OFF_SITE_WARNING =
	"The built-in storage runs on the same host as the control plane. If that host is lost, these backups are lost with it. Add an external destination, such as an S3 bucket elsewhere, to keep a copy off-site.";

/** Whether to warn that the backups are not off-site, from the view's flag. */
export function notOffSite(
	view: Pick<InstanceBackupView, "notOffSite">,
): boolean {
	return view.notOffSite;
}

/** The name of a destination kind, in the section and the history. */
export function destKindLabel(kind: DestKind): string {
	switch (kind) {
		case "s3":
			return "S3-compatible bucket";
		case "local":
			return "Directory on this host";
		case "builtin":
			return "Built-in storage";
	}
}
