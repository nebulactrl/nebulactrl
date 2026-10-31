import type { SetMaintenanceInputBody } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { relativeTime } from "#/lib/format";

/** Server limits of the page text. */
export const MAX_TITLE = 80;
export const MAX_MESSAGE = 600;

export const DEFAULT_TITLE = "Down for maintenance";
export const DEFAULT_MESSAGE = "This service is being updated. Back soon.";

/**
 * The working copy of the form. `eta` is local wall-clock time as
 * `YYYY-MM-DDTHH:mm` with no zone, or a partly typed value while the date and
 * time fields are being filled in (it then fails validation until complete).
 */
export interface MaintenanceFormState {
	title: string;
	message: string;
	eta: string;
}

export type MaintenanceFormErrors = Partial<
	Record<"title" | "message" | "eta", string>
>;

const pad = (n: number) => n.toString().padStart(2, "0");

/** A Date as `YYYY-MM-DDTHH:mm` (local time, minutes). */
export function toLocalInput(date: Date): string {
	return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

const LOCAL_INPUT = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/;

/** The instant a `YYYY-MM-DDTHH:mm` value means in the browser's zone; null when empty, partial or not a real date. */
export function fromLocalInput(value: string): Date | null {
	const match = LOCAL_INPUT.exec(value);
	if (!match) return null;
	const [year, month, day, hour, minute] = match.slice(1).map(Number) as [
		number,
		number,
		number,
		number,
		number,
	];
	const date = new Date(year, month - 1, day, hour, minute);
	// A rolled-over date (30 February, 25:00) is not what was typed.
	return date.getFullYear() === year &&
		date.getMonth() === month - 1 &&
		date.getDate() === day &&
		date.getHours() === hour &&
		date.getMinutes() === minute
		? date
		: null;
}

/** The date and time fields' text for an `eta` value. */
export function splitLocalInput(value: string): { date: string; time: string } {
	const at = value.indexOf("T");
	return at < 0
		? { date: value, time: "" }
		: { date: value.slice(0, at), time: value.slice(at + 1) };
}

/** The `eta` value for the date and time fields; empty when both are. */
export function joinLocalInput(date: string, time: string): string {
	const d = date.trim();
	const t = time.trim();
	return d === "" && t === "" ? "" : `${d}T${t}`;
}

/** The zone the times are read in, for the line under the fields. */
export function zoneNote(now: Date = new Date()): string {
	const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
	const offset = new Intl.DateTimeFormat(undefined, {
		timeZoneName: "short",
	})
		.formatToParts(now)
		.find((part) => part.type === "timeZoneName")?.value;
	return zone && offset && zone !== offset
		? `${zone} (${offset})`
		: (zone ?? offset ?? "your local time zone");
}

/** The form as the server currently has it (an ETA in the past starts empty). */
export function toFormState(
	m: Service["maintenance"],
	now: Date = new Date(),
): MaintenanceFormState {
	const eta = m.eta ? new Date(m.eta) : null;
	return {
		title: m.title,
		message: m.message,
		eta: eta && eta.getTime() > now.getTime() ? toLocalInput(eta) : "",
	};
}

/** Mirrors the server's validation so a bad value never leaves the browser. */
export function validateForm(
	state: MaintenanceFormState,
	now: Date = new Date(),
): MaintenanceFormErrors {
	const errors: MaintenanceFormErrors = {};
	if ([...state.title.trim()].length > MAX_TITLE) {
		errors.title = `The title can be at most ${MAX_TITLE} characters.`;
	}
	if ([...state.message.trim()].length > MAX_MESSAGE) {
		errors.message = `The message can be at most ${MAX_MESSAGE} characters.`;
	}
	if (state.eta !== "") {
		const eta = fromLocalInput(state.eta);
		if (!eta) errors.eta = "Enter a valid date and time.";
		else if (eta.getTime() <= now.getTime()) {
			errors.eta = "The expected return time must be in the future.";
		}
	}
	return errors;
}

/** The request that turns maintenance on with this page text. */
export function buildOnBody(
	state: MaintenanceFormState,
): SetMaintenanceInputBody {
	const body: SetMaintenanceInputBody = { enabled: true };
	const title = state.title.trim();
	const message = state.message.trim();
	if (title) body.title = title;
	if (message) body.message = message;
	const eta = fromLocalInput(state.eta);
	if (eta) body.eta = eta.toISOString();
	return body;
}

export const OFF_BODY: SetMaintenanceInputBody = { enabled: false };

/** True when the form differs from what the live page says. */
export function isDirty(
	m: Service["maintenance"],
	state: MaintenanceFormState,
	now: Date = new Date(),
): boolean {
	const base = toFormState(m, now);
	return (
		base.title !== state.title.trim() ||
		base.message !== state.message.trim() ||
		base.eta !== state.eta
	);
}

/** The preview page for what is typed in the form (unsaved text included). */
export function previewPath(
	serviceId: string,
	state: MaintenanceFormState,
): string {
	const q = new URLSearchParams({ draft: "true" });
	if (state.title.trim()) q.set("title", state.title.trim());
	if (state.message.trim()) q.set("message", state.message.trim());
	const eta = fromLocalInput(state.eta);
	if (eta) q.set("eta", eta.toISOString());
	return `/api/v1/services/${serviceId}/maintenance/preview?${q}`;
}

/** Quick expected-return choices: minutes from now. */
export const ETA_PRESETS = [
	{ label: "30 min", minutes: 30 },
	{ label: "1 hour", minutes: 60 },
	{ label: "4 hours", minutes: 240 },
	{ label: "Tomorrow", minutes: 24 * 60 },
] as const;

export function etaAfter(minutes: number, now: Date = new Date()): string {
	return toLocalInput(new Date(now.getTime() + minutes * 60_000));
}

/** Whether the service can be put into maintenance: it serves a domain over HTTP. */
export function maintenanceSupported(service: Service): boolean {
	if (service.domains.length === 0) return false;
	const route = service.kind === "route" ? service.config.route : undefined;
	return !route?.protocol || route.protocol === "http";
}

export function isInMaintenance(service: Service): boolean {
	return service.maintenance?.enabled === true;
}

/** One line for the settings section header. */
export function maintenanceSummary(m: Service["maintenance"]): string {
	if (!m.enabled) return "Off";
	const since = relativeTime(m.since);
	const eta = m.eta ? new Date(m.eta) : null;
	const back =
		eta && !Number.isNaN(eta.getTime())
			? ` · back around ${eta.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}`
			: "";
	return `On${since ? ` since ${since}` : ""}${back}`;
}
