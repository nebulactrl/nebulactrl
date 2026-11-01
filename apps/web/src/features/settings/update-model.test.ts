import { describe, expect, test } from "bun:test";
import {
	canStart,
	chipLabel,
	elapsedMs,
	formatElapsed,
	GIVE_UP_MS,
	IDLE_POLL_MS,
	idleSummary,
	isStalled,
	isToastDue,
	isWaitingForRestart,
	parseMarker,
	pollInterval,
	type ReloadMarker,
	RUNNING_POLL_MS,
	STALLED_POLL_MS,
	shouldReload,
	showsManualCommand,
	type UpdateInfo,
	type UpdateRun,
	updatedToastTitle,
	updateWaiting,
	versionLabel,
	viewState,
} from "./update-model";

const NOW = Date.parse("2026-10-29T12:00:00Z");

function info(overrides: Partial<UpdateInfo> = {}): UpdateInfo {
	return {
		current: "0.1.0",
		available: false,
		checkEnabled: true,
		canUpdate: true,
		isAdmin: true,
		manualCommand: "curl -fsSL https://nebulactrl.com/install.sh | sudo bash",
		...overrides,
	};
}

function run(overrides: Partial<UpdateRun> = {}): UpdateRun {
	return {
		state: "running",
		fromVersion: "0.1.0",
		targetVersion: "0.1.2",
		startedAt: "2026-10-29T11:59:00Z",
		...overrides,
	};
}

describe("versionLabel", () => {
	test("a release number gets one v", () => {
		expect(versionLabel("0.1.2")).toBe("v0.1.2");
		expect(versionLabel("v0.1.2")).toBe("v0.1.2");
	});

	test("a development build stays a word", () => {
		expect(versionLabel("dev")).toBe("dev");
	});
});

describe("chipLabel", () => {
	test("names the newer release", () => {
		expect(chipLabel(info({ available: true, latest: "0.1.2" }))).toBe(
			"v0.1.2 available",
		);
	});

	test("is absent while loading, errored or not newer", () => {
		expect(chipLabel(undefined)).toBeNull();
		expect(chipLabel(info({ latest: "0.1.0" }))).toBeNull();
		expect(chipLabel(info({ available: true }))).toBeNull();
	});

	test("is only for the instance admin", () => {
		expect(
			chipLabel(info({ available: true, latest: "0.1.2", isAdmin: false })),
		).toBeNull();
	});
});

describe("updateWaiting", () => {
	test("a newer release for the instance admin", () => {
		expect(updateWaiting(info({ available: true, latest: "0.1.2" }))).toBe(
			true,
		);
	});

	test("nothing waits for anyone else, or when up to date", () => {
		expect(updateWaiting(undefined)).toBe(false);
		expect(updateWaiting(info({ latest: "0.1.0" }))).toBe(false);
		expect(
			updateWaiting(info({ available: true, latest: "0.1.2", isAdmin: false })),
		).toBe(false);
	});
});

describe("viewState", () => {
	test("up to date", () => {
		expect(viewState(info({ latest: "0.1.0" }))).toBe("idle");
	});

	test("a newer release the caller may install", () => {
		expect(viewState(info({ available: true, latest: "0.1.2" }))).toBe(
			"available",
		);
	});

	test("a newer release the caller may not install", () => {
		expect(
			viewState(info({ available: true, latest: "0.1.2", canUpdate: false })),
		).toBe("unavailable");
	});

	test("checks off, with nothing announced", () => {
		expect(viewState(info({ checkEnabled: false }))).toBe("checks-off");
	});

	test("a running update wins over a newer release", () => {
		expect(
			viewState(info({ available: true, latest: "0.1.2", updating: run() })),
		).toBe("running");
	});

	test("a failed update is shown with its error", () => {
		expect(
			viewState(
				info({
					available: true,
					latest: "0.1.2",
					updating: run({ state: "failed", error: "rolled back" }),
				}),
			),
		).toBe("failed");
	});

	test("a finished update falls back to the plain state", () => {
		expect(
			viewState(
				info({
					current: "0.1.2",
					latest: "0.1.2",
					updating: run({ state: "succeeded" }),
				}),
			),
		).toBe("idle");
	});
});

describe("canStart and the manual command", () => {
	test("the button needs a newer release and permission", () => {
		expect(canStart(info({ available: true, latest: "0.1.2" }))).toBe(true);
		expect(
			canStart(info({ available: true, latest: "0.1.2", canUpdate: false })),
		).toBe(false);
		expect(canStart(info({ latest: "0.1.0" }))).toBe(false);
		expect(canStart(info({ available: true }))).toBe(false);
	});

	test("the command shows where there is no button", () => {
		expect(showsManualCommand("unavailable", false)).toBe(true);
		expect(showsManualCommand("checks-off", false)).toBe(true);
		expect(showsManualCommand("available", true)).toBe(false);
		expect(showsManualCommand("running", false)).toBe(false);
		expect(showsManualCommand("idle", false)).toBe(false);
	});

	test("a failed update shows it only when it cannot be retried here", () => {
		expect(showsManualCommand("failed", false)).toBe(true);
		expect(showsManualCommand("failed", true)).toBe(false);
	});

	test("the idle sentence says why nothing is known", () => {
		expect(idleSummary(info({ latest: "0.1.0" }))).toBe(
			"You are on the latest version.",
		);
		expect(idleSummary(info())).toContain("No check");
		expect(idleSummary(info({ current: "dev" }))).toContain(
			"development build",
		);
	});
});

describe("elapsed time", () => {
	test("formats seconds, minutes and hours", () => {
		expect(formatElapsed(0)).toBe("0s");
		expect(formatElapsed(42_900)).toBe("42s");
		expect(formatElapsed(65_000)).toBe("1m 05s");
		expect(formatElapsed(12 * 60_000 + 3_000)).toBe("12m 03s");
		expect(formatElapsed(3_720_000)).toBe("1h 02m");
	});

	test("never negative, never NaN", () => {
		expect(formatElapsed(-5_000)).toBe("0s");
		expect(elapsedMs("2026-10-29T12:00:05Z", NOW)).toBe(0);
		expect(elapsedMs("not a time", NOW)).toBe(0);
		expect(elapsedMs("2026-10-29T11:59:00Z", NOW)).toBe(60_000);
	});
});

describe("giving up", () => {
	test("a run past 30 minutes is stalled", () => {
		const started = new Date(NOW - GIVE_UP_MS - 1_000).toISOString();
		expect(isStalled(run({ startedAt: started }), NOW)).toBe(true);
		expect(isStalled(run(), NOW)).toBe(false);
	});

	test("only a running update can stall", () => {
		const started = new Date(NOW - GIVE_UP_MS - 1_000).toISOString();
		expect(isStalled(run({ state: "failed", startedAt: started }), NOW)).toBe(
			false,
		);
		expect(isStalled(undefined, NOW)).toBe(false);
	});

	test("the status is read fast while running, slowly otherwise", () => {
		expect(pollInterval(info({ updating: run() }), NOW)).toBe(RUNNING_POLL_MS);
		const started = new Date(NOW - GIVE_UP_MS - 1_000).toISOString();
		const stalled = info({ updating: run({ startedAt: started }) });
		expect(pollInterval(stalled, NOW)).toBe(STALLED_POLL_MS);
		expect(pollInterval(info(), NOW)).toBe(IDLE_POLL_MS);
		expect(pollInterval(undefined, NOW)).toBe(IDLE_POLL_MS);
	});
});

describe("waiting for the restart", () => {
	test("a dead connection or a proxy error mid-update is a restart", () => {
		const running = info({ updating: run() });
		for (const status of [0, 502, 503, 504]) {
			expect(isWaitingForRestart(running, status)).toBe(true);
		}
	});

	test("other errors, or no update, are not", () => {
		expect(isWaitingForRestart(info({ updating: run() }), 500)).toBe(false);
		expect(isWaitingForRestart(info({ updating: run() }), undefined)).toBe(
			false,
		);
		expect(isWaitingForRestart(info(), 502)).toBe(false);
		expect(isWaitingForRestart(undefined, 502)).toBe(false);
	});
});

describe("reload guard", () => {
	const marker = (version: string, toasted = false): ReloadMarker => ({
		version,
		toasted,
	});

	test("reloads once when the server reports a new version", () => {
		expect(
			shouldReload({ pageVersion: "0.1.0", current: "0.1.2", marker: null }),
		).toBe(true);
	});

	test("does not reload for the version the page already has", () => {
		expect(
			shouldReload({ pageVersion: "0.1.2", current: "v0.1.2", marker: null }),
		).toBe(false);
	});

	test("does not reload before the page knows its own version", () => {
		expect(
			shouldReload({
				pageVersion: undefined,
				current: "0.1.2",
				marker: null,
			}),
		).toBe(false);
	});

	test("never loops: a marker for that version stops the reload", () => {
		expect(
			shouldReload({
				pageVersion: "0.1.0",
				current: "0.1.2",
				marker: marker("0.1.2"),
			}),
		).toBe(false);
		expect(
			shouldReload({
				pageVersion: "0.1.0",
				current: "0.1.3",
				marker: marker("0.1.2"),
			}),
		).toBe(true);
	});

	test("the toast is due once, for the marked version", () => {
		expect(isToastDue(marker("0.1.2"), "0.1.2")).toBe(true);
		expect(isToastDue(marker("0.1.2", true), "0.1.2")).toBe(false);
		expect(isToastDue(marker("0.1.2"), "0.1.3")).toBe(false);
		expect(isToastDue(null, "0.1.2")).toBe(false);
	});

	test("reads the marker back, and ignores anything else", () => {
		expect(parseMarker('{"version":"0.1.2","toasted":true}')).toEqual(
			marker("0.1.2", true),
		);
		expect(parseMarker('{"version":"0.1.2"}')).toEqual(marker("0.1.2"));
		expect(parseMarker(null)).toBeNull();
		expect(parseMarker("nope")).toBeNull();
		expect(parseMarker('{"version":3}')).toBeNull();
		expect(parseMarker("[]")).toBeNull();
	});

	test("the toast names the version", () => {
		expect(updatedToastTitle("0.1.2")).toBe("Updated to v0.1.2");
	});
});
