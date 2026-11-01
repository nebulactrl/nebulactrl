import type { GetSystemUpdateResponse } from "#/api/gen/types.gen";

/** `GET /system/update`: what is installed, what the release feed announced, and any update in flight. */
export type UpdateInfo = GetSystemUpdateResponse;
export type UpdateRun = NonNullable<UpdateInfo["updating"]>;

/** While an update runs the status is read this often. */
export const RUNNING_POLL_MS = 3_000;
/** A run that has not ended after this long is given up on: reads slow down and the card says so. */
export const GIVE_UP_MS = 30 * 60_000;
/** After giving up, and when nothing runs, the notice is refreshed this rarely. */
export const STALLED_POLL_MS = 30_000;
export const IDLE_POLL_MS = 15 * 60_000;

/** Whether a newer release is out and this caller (the instance admin) can act on it. */
export function updateWaiting(info: UpdateInfo | undefined): boolean {
	return Boolean(info?.isAdmin && info.available && info.latest);
}

/** "v0.1.2" for a release number, the word itself for anything else ("dev"). */
export function versionLabel(version: string): string {
	const bare = bareVersion(version);
	return /^\d/.test(bare) ? `v${bare}` : bare;
}

/** "0.1.2" for "v0.1.2" or "0.1.2". */
export function bareVersion(version: string): string {
	return version.trim().replace(/^v/i, "");
}

/**
 * The sidebar chip: "v0.1.2 available", or null when there is nothing newer
 * to say or the caller is not the instance admin (only they can update).
 */
export function chipLabel(info: UpdateInfo | undefined): string | null {
	if (!info || !updateWaiting(info) || !info.latest) return null;
	return `${versionLabel(info.latest)} available`;
}

export type ViewState =
	| "idle"
	| "available"
	| "running"
	| "failed"
	| "unavailable"
	| "checks-off";

/**
 * What the Version card shows. An update in flight or just failed wins over
 * everything else; then a newer release (with the button only for someone who
 * can use it); then the notice that checks are off; otherwise "up to date".
 */
export function viewState(info: UpdateInfo): ViewState {
	const run = info.updating;
	if (run?.state === "running") return "running";
	if (run?.state === "failed") return "failed";
	if (info.available) return info.canUpdate ? "available" : "unavailable";
	if (!info.checkEnabled) return "checks-off";
	return "idle";
}

/** The button is offered only for a newer release this caller may install. */
export function canStart(info: UpdateInfo): boolean {
	return info.available && info.canUpdate && Boolean(info.latest);
}

/**
 * The manual command is shown where there is no button to press: a newer
 * release this person cannot install, checks that are off, or a failed update
 * that cannot be retried from here.
 */
export function showsManualCommand(
	state: ViewState,
	startable: boolean,
): boolean {
	if (state === "failed") return !startable;
	return state === "unavailable" || state === "checks-off";
}

/** One sentence for the idle state: up to date, or why nothing is known yet. */
export function idleSummary(info: UpdateInfo): string {
	if (!/^\d/.test(bareVersion(info.current))) {
		return "This is a development build, so it is not compared with releases.";
	}
	if (!info.latest) {
		return "No check for new releases has completed yet. The control plane checks once a day.";
	}
	return "You are on the latest version.";
}

/** Milliseconds since `startedAt`; 0 for an unreadable time or one slightly ahead of this clock. */
export function elapsedMs(startedAt: string, now: number): number {
	const started = Date.parse(startedAt);
	return Number.isNaN(started) ? 0 : Math.max(0, now - started);
}

/** "42s", "3m 05s", "1h 02m". */
export function formatElapsed(ms: number): string {
	const total = Math.floor(Math.max(0, ms) / 1_000);
	const hours = Math.floor(total / 3_600);
	const minutes = Math.floor((total % 3_600) / 60);
	const seconds = total % 60;
	const pad = (value: number) => value.toString().padStart(2, "0");
	if (hours > 0) return `${hours}h ${pad(minutes)}m`;
	if (minutes > 0) return `${minutes}m ${pad(seconds)}s`;
	return `${seconds}s`;
}

/** A running update that has outlived the give-up window. */
export function isStalled(run: UpdateRun | undefined, now: number): boolean {
	return run?.state === "running" && elapsedMs(run.startedAt, now) > GIVE_UP_MS;
}

/** How soon to read the status again. */
export function pollInterval(
	info: UpdateInfo | undefined,
	now: number,
): number {
	const run = info?.updating;
	if (run?.state !== "running") return IDLE_POLL_MS;
	return isStalled(run, now) ? STALLED_POLL_MS : RUNNING_POLL_MS;
}

/**
 * The control plane restarting looks like a dead connection (status 0) or the
 * proxy in front of it answering 502, 503 or 504. Anything else is a real
 * error, not "restarting".
 */
function isRestartStatus(status: number | undefined): boolean {
	return status === 0 || status === 502 || status === 503 || status === 504;
}

/** An update is running and the last read failed the way a restart does. */
export function isWaitingForRestart(
	info: UpdateInfo | undefined,
	errorStatus: number | undefined,
): boolean {
	return info?.updating?.state === "running" && isRestartStatus(errorStatus);
}

/** The page's record that it already reloaded for a version, and whether it said so. */
export interface ReloadMarker {
	version: string;
	toasted: boolean;
}

/** `sessionStorage` key of the reload marker. */
export const MARKER_KEY = "nebulactrl.updated";

export function parseMarker(raw: string | null): ReloadMarker | null {
	if (!raw) return null;
	try {
		const value: unknown = JSON.parse(raw);
		if (typeof value !== "object" || value === null) return null;
		const { version, toasted } = value as Record<string, unknown>;
		if (typeof version !== "string" || version === "") return null;
		return { version, toasted: toasted === true };
	} catch {
		return null;
	}
}

/**
 * Whether this page is showing an old UI. `pageVersion` is the control-plane
 * version the page first heard of; once the server reports a different one the
 * served bundle has changed too, so the page reloads once. A marker for that
 * version means it already did, and a loop is never started.
 */
export function shouldReload({
	pageVersion,
	current,
	marker,
}: {
	pageVersion: string | undefined;
	current: string;
	marker: ReloadMarker | null;
}): boolean {
	if (pageVersion === undefined) return false;
	if (bareVersion(current) === bareVersion(pageVersion)) return false;
	return marker?.version !== bareVersion(current);
}

/** After the reload: say so once for the version the marker names. */
export function isToastDue(
	marker: ReloadMarker | null,
	current: string,
): boolean {
	return (
		marker !== null &&
		!marker.toasted &&
		marker.version === bareVersion(current)
	);
}

export function updatedToastTitle(version: string): string {
	return `Updated to ${versionLabel(version)}`;
}
