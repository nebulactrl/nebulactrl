import type {
	AutoUpdateInputBody,
	PatchAuto,
	PatchAutoRun,
	PatchPlan,
	PatchRun,
	PatchView,
	ServerPatchSummary,
} from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";
import { isValidTimezone } from "#/features/tasks/model";
import { count } from "#/lib/format";

/** The shortest honest summary of the pending updates: "3 updates (1 security)". */
export function updatesText(
	s: Pick<ServerPatchSummary, "known" | "supported" | "updates" | "security">,
): string {
	if (!s.known) return "Updates not checked yet";
	if (!s.supported) return "Updates not managed here";
	if (s.updates === 0) return "Up to date";
	return `${count(s.updates, "update")} (${s.security} security)`;
}

/**
 * The status of the update line: security updates warn, other updates are a
 * still "pending" mark. Never a spinner: nothing runs until someone applies them.
 */
export function updatesStatus(
	s: Pick<ServerPatchSummary, "known" | "supported" | "updates" | "security">,
): StatusKind {
	if (!s.known || !s.supported) return "unknown";
	if (s.security > 0) return "warning";
	return s.updates > 0 ? "pending" : "healthy";
}

/** Whether the server card needs to say anything about patching. */
export function needsAttention(
	s: Pick<
		ServerPatchSummary,
		"known" | "supported" | "updates" | "rebootRequired"
	>,
): boolean {
	return s.known && s.supported && (s.updates > 0 || s.rebootRequired);
}

/** The Updates tab's badge: "Reboot" first, then the security count; null when quiet. */
export function tabBadge(
	s: Pick<
		ServerPatchSummary,
		"known" | "supported" | "security" | "rebootRequired"
	>,
): string | null {
	if (!s.known || !s.supported) return null;
	if (s.rebootRequired) return "Reboot";
	return s.security > 0 ? `${s.security} security` : null;
}

/** Words for the run in flight, or null. */
export function busyText(busy: ServerPatchSummary["busy"]): string | null {
	switch (busy) {
		case "check":
			return "Checking for updates";
		case "apply":
			return "Upgrading packages";
		case "reboot":
			return "Rebooting";
		default:
			return null;
	}
}

/** Why the Check / Apply / Reboot buttons are disabled; null when they work. */
export function patchHint(
	view: Pick<PatchView, "agentSupported" | "busy" | "supported" | "known">,
	serverStatus: string,
	opts: {
		canCheck: boolean;
		instanceAdmin: boolean;
		action: "check" | "apply" | "reboot";
	},
): string | null {
	if (!view.agentSupported)
		return "This agent is too old to manage updates. Update it first.";
	if (serverStatus === "revoked") return "This server was revoked.";
	if (serverStatus !== "online")
		return "The server is offline. Actions start when it is back online.";
	if (view.busy) return `${busyText(view.busy)} is in progress.`;
	if (opts.action === "check") {
		return opts.canCheck ? null : "Only Owners and Admins can check a host.";
	}
	if (!opts.instanceAdmin)
		return "Only the instance admin can upgrade packages or reboot a server.";
	if (opts.action === "apply") {
		if (!view.known) return "Check for updates first.";
		if (!view.supported)
			return "This host has no supported package manager (apt or dnf).";
	}
	return null;
}

const ACTION_LABEL: Record<PatchRun["action"], string> = {
	check: "Check",
	apply: "Upgrade",
	reboot: "Reboot",
};

export function runActionLabel(run: Pick<PatchRun, "action">): string {
	return ACTION_LABEL[run.action];
}

export function triggerText(run: Pick<PatchRun, "trigger">): string {
	return run.trigger === "scheduled" ? "Automatic" : "By hand";
}

/** The status icon and words of a run. */
export function runStatus(run: Pick<PatchRun, "status" | "action">): {
	kind: StatusKind;
	label: string;
} {
	switch (run.status) {
		case "pending":
			return { kind: "building", label: "Running" };
		case "rebooting":
			return { kind: "building", label: "Waiting for the server to come back" };
		case "succeeded":
			return { kind: "healthy", label: "Done" };
		default:
			return { kind: "failed", label: "Failed" };
	}
}

/** One line for a finished run. */
export function runSummary(run: PatchRun): string {
	if (run.status === "failed") return run.error || "Failed";
	if (run.status === "pending") return "Running";
	if (run.status === "rebooting") return run.message || "Rebooting";
	switch (run.action) {
		case "apply": {
			const before = run.updatesBefore;
			const after = run.updatesAfter;
			if (before !== null && after !== null)
				return `${count(before, "update")} before, ${after} left`;
			return "Packages upgraded";
		}
		case "reboot":
			return run.message || "Rebooted";
		default:
			return run.updatesAfter === null
				? "Checked"
				: run.updatesAfter === 0
					? "Up to date"
					: `${count(run.updatesAfter, "update")} pending`;
	}
}

/** A server with no production service and not the control plane needs no acknowledgement. */
export function needsAcknowledgement(
	view: Pick<PatchView, "productionServices" | "controlPlane">,
): boolean {
	return view.productionServices.length > 0 || view.controlPlane;
}

/** "api, worker and 3 more" from the production services on the host. */
export function productionList(
	services: ReadonlyArray<{ name: string; project: string }>,
	max = 4,
): string {
	const names = services.slice(0, max).map((s) => `${s.project}/${s.name}`);
	const rest = services.length - names.length;
	return rest > 0 ? `${names.join(", ")} and ${rest} more` : names.join(", ");
}

/** The warnings a reboot confirmation must show. */
export function rebootWarnings(
	view: Pick<
		PatchView,
		"productionServices" | "controlPlane" | "rebootRequired"
	>,
	serviceCount: number,
): string[] {
	const out: string[] = [];
	out.push(
		serviceCount > 0
			? `${count(serviceCount, "service")} on this server go down while it reboots and start again when Docker is back.`
			: "Anything that runs on this server goes down while it reboots.",
	);
	if (view.productionServices.length > 0) {
		out.push(
			`Production is affected: ${productionList(view.productionServices)}.`,
		);
	}
	if (view.controlPlane) {
		out.push(
			"This server runs NebulaCtrl itself. The dashboard and API are unavailable until it is back.",
		);
	}
	out.push(
		"NebulaCtrl waits for the agent to reconnect on a new boot and reports a failure if that takes more than 20 minutes.",
	);
	return out;
}

/** Reasons the plan cannot be applied, or the packages that need a closer look. */
export function planCautions(plan: PatchPlan): string[] {
	return [
		...plan.risks.map((r) => `${r.package}: ${r.reason}`),
		...plan.warnings,
	];
}

/** Window lengths the form offers, in minutes (the API takes 60-240). */
export const WINDOW_LENGTHS: readonly number[] = [60, 120, 180, 240];

/** The most extra packages a schedule may hold back. */
export const HOLD_LIMIT = 20;

/** What a reboot inside the window needs; the same guards the docs list. */
export const REBOOT_GUARDS =
	"Reboots only when: at least 10 minutes of the window are left · no automatic reboot in the last 24 hours · this is not the NebulaCtrl server · every running container restarts by itself (always / unless-stopped). Otherwise it installs and says why it did not reboot.";

/** The form's copy of the automatic-update settings. */
export interface AutoDraft {
	enabled: boolean;
	scope: PatchAuto["scope"];
	windowStart: string;
	windowMinutes: number;
	timezone: string;
	reboot: PatchAuto["reboot"];
	holdPackages: string[];
}

/**
 * The saved settings as a form draft. On a host whose package manager cannot
 * limit an upgrade to security updates the scope reads "all": the only value
 * the API would accept.
 */
export function autoDraftOf(auto: PatchAuto): AutoDraft {
	return {
		enabled: auto.enabled,
		scope: auto.securityOnlySupported ? auto.scope : "all",
		windowStart: auto.windowStart,
		windowMinutes: auto.windowMinutes,
		timezone: auto.timezone,
		reboot: auto.reboot,
		holdPackages: [...auto.holdPackages],
	};
}

/** The draft as the API takes it: names trimmed, blanks and repeats dropped. */
export function autoBody(
	draft: AutoDraft,
	acknowledgeProduction = false,
): AutoUpdateInputBody {
	const holds: string[] = [];
	for (const raw of draft.holdPackages) {
		const name = raw.trim();
		if (name !== "" && !holds.includes(name)) holds.push(name);
	}
	return {
		enabled: draft.enabled,
		scope: draft.scope,
		windowStart: draft.windowStart.trim(),
		windowMinutes: draft.windowMinutes,
		timezone: draft.timezone.trim() || "UTC",
		reboot: draft.reboot,
		holdPackages: holds,
		...(acknowledgeProduction ? { acknowledgeProduction: true } : {}),
	};
}

/** A stable key to compare drafts by what would be sent, not by how it is typed. */
export function autoKey(draft: AutoDraft): string {
	return JSON.stringify(autoBody(draft));
}

/**
 * Switching the schedule on. A server that still has the default zone (UTC) gets
 * the reader's own zone, so "03:00" means the reader's night, unless the reader
 * already picked a zone in the draft.
 */
export function enableAuto(
	draft: AutoDraft,
	saved: Pick<PatchAuto, "enabled" | "timezone">,
	browserZone: string,
): AutoDraft {
	const untouched = !saved.enabled && saved.timezone === draft.timezone;
	const zone =
		untouched &&
		draft.timezone === "UTC" &&
		browserZone !== "" &&
		isValidTimezone(browserZone)
			? browserZone
			: draft.timezone;
	return { ...draft, enabled: true, timezone: zone };
}

/**
 * The server's quiet-hour suggestion is a start in the zone the server has
 * saved, so taking it brings that zone along.
 */
export function applySuggestion(
	draft: AutoDraft,
	auto: Pick<PatchAuto, "suggestedStart" | "timezone">,
): AutoDraft {
	if (!auto.suggestedStart) return draft;
	return {
		...draft,
		windowStart: auto.suggestedStart,
		timezone: auto.timezone,
	};
}

/** The lengths to offer: the usual ones plus a saved length that is none of them. */
export function windowLengths(current: number): number[] {
	return WINDOW_LENGTHS.includes(current)
		? [...WINDOW_LENGTHS]
		: [...WINDOW_LENGTHS, current].sort((a, b) => a - b);
}

/** "1h", "2h", "90m". */
export function windowLengthLabel(minutes: number): string {
	return minutes % 60 === 0 ? `${minutes / 60}h` : `${minutes}m`;
}

/** What is wrong with the draft, by field. Empty when it can be sent. */
export interface AutoIssues {
	windowStart?: string;
	timezone?: string;
	holdPackages?: string;
	/** Per row of `holdPackages`, by index. */
	holdRows: Record<number, string>;
}

const START_PATTERN = /^([01]\d|2[0-3]):[0-5]\d$/;
const PACKAGE_PATTERN = /^[A-Za-z0-9][A-Za-z0-9+._:-]*$/;

export function autoIssues(draft: AutoDraft): AutoIssues {
	const issues: AutoIssues = { holdRows: {} };
	if (!START_PATTERN.test(draft.windowStart.trim())) {
		issues.windowStart = "Enter a start time as HH:MM, 24 hours.";
	}
	if (!isValidTimezone(draft.timezone)) {
		issues.timezone = "This is not a time zone the browser knows.";
	}
	draft.holdPackages.forEach((raw, index) => {
		const name = raw.trim();
		if (name !== "" && !PACKAGE_PATTERN.test(name)) {
			issues.holdRows[index] =
				"Use a package name such as openssl: letters, digits and . + - _ :";
		}
	});
	if (autoBody(draft).holdPackages.length > HOLD_LIMIT) {
		issues.holdPackages = `Hold at most ${HOLD_LIMIT} packages.`;
	}
	return issues;
}

export function hasAutoIssues(issues: AutoIssues): boolean {
	return (
		issues.windowStart !== undefined ||
		issues.timezone !== undefined ||
		issues.holdPackages !== undefined ||
		Object.keys(issues.holdRows).length > 0
	);
}

/** Clocks change between 01:00 and 02:59 in many zones; null when the start is safe. */
export function startWarning(windowStart: string): string | null {
	const start = windowStart.trim();
	if (!START_PATTERN.test(start)) return null;
	const hour = Number(start.slice(0, 2));
	return hour === 1 || hour === 2
		? "Clocks change between 01:00 and 02:59 in many zones. Pick a start outside it."
		: null;
}

/** The form field a 422 from the save belongs to; null when it concerns the whole schedule. */
export function autoErrorField(
	code: string | undefined,
): "windowStart" | "timezone" | "scope" | "holdPackages" | null {
	switch (code) {
		case "invalid_window":
			return "windowStart";
		case "invalid_timezone":
			return "timezone";
		case "scope_unsupported":
			return "scope";
		case "invalid_package":
			return "holdPackages";
		default:
			return null;
	}
}

const MONTHS = [
	"Jan",
	"Feb",
	"Mar",
	"Apr",
	"May",
	"Jun",
	"Jul",
	"Aug",
	"Sep",
	"Oct",
	"Nov",
	"Dec",
];

/**
 * When the next window starts, in the schedule's zone: "Tonight 03:12 Europe/Berlin",
 * "Tomorrow 14:00 UTC", else "Tue 27 Oct 03:12 Europe/Berlin". `now` pins the
 * clock for tests.
 */
export function nextWindowText(
	iso: string,
	zone: string,
	now: Date = new Date(),
): string {
	const at = new Date(iso);
	if (Number.isNaN(at.getTime())) return "";
	const tz = zone.trim() !== "" && isValidTimezone(zone) ? zone.trim() : "UTC";
	const read = (date: Date) => {
		const parts = new Intl.DateTimeFormat("en-GB", {
			timeZone: tz,
			weekday: "short",
			day: "numeric",
			month: "short",
			year: "numeric",
			hour: "2-digit",
			minute: "2-digit",
			hourCycle: "h23",
		}).formatToParts(date);
		const get = (type: string) =>
			parts.find((part) => part.type === type)?.value ?? "";
		return {
			day: Date.UTC(
				Number(get("year")),
				MONTHS.indexOf(get("month")),
				Number(get("day")),
			),
			hour: Number(get("hour")),
			label: `${get("weekday")} ${get("day")} ${get("month")}`,
			time: `${get("hour")}:${get("minute")}`,
		};
	};
	const when = read(at);
	const days = Math.round((when.day - read(now).day) / 86_400_000);
	if (days === 0) {
		return `${when.hour >= 18 ? "Tonight" : "Today"} ${when.time} ${tz}`;
	}
	if (days === 1) {
		return `${when.hour < 6 ? "Tonight" : "Tomorrow"} ${when.time} ${tz}`;
	}
	return `${when.label} ${when.time} ${tz}`;
}

/** The last scheduled run's state in a word. */
export function autoRunStatusText(run: Pick<PatchAutoRun, "status">): string {
	switch (run.status) {
		case "pending":
			return "Running";
		case "rebooting":
			return "Rebooting";
		case "succeeded":
			return "Succeeded";
		default:
			return "Failed";
	}
}

/** "2 failed runs in a row"; null when none. Says what the third does. */
export function failuresText(failures: number): string | null {
	if (failures <= 0) return null;
	return `${count(failures, "failed run")} in a row. Three in a row switch automatic updates off.`;
}

/** The note about the host's own updater, which NebulaCtrl takes over. */
export function nativeUpdatesText(
	native: PatchAuto["nativeAutoUpdates"],
	enabled: boolean,
): string | null {
	if (native === "") return null;
	return enabled
		? `NebulaCtrl turns off ${native} on this server while automatic updates are on.`
		: `${native} is active on this server. NebulaCtrl turns it off while automatic updates are on and turns it back on when you switch them off.`;
}
