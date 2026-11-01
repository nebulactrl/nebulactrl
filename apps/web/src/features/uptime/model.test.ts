import { describe, expect, test } from "bun:test";
import {
	attentionCount,
	buildCells,
	buildMonitorBody,
	cellError,
	cellTime,
	cellTone,
	checksLine,
	checksSpan,
	checksStoppedAt,
	emptyRunTime,
	emptyRunWords,
	failingHint,
	formatAvailability,
	formatSpan,
	formatUtcStamp,
	latencySummary,
	monitorChanged,
	type PointLike,
	readoutRows,
	reminderText,
	rowNote,
	serviceStateView,
	sortServices,
	stateSeverity,
	stateView,
	stripSummary,
	toMonitorForm,
	tonesPresent,
	uptimeDays,
	validateMonitor,
} from "./model";

const point = (patch: Partial<PointLike> = {}): PointLike => ({
	at: "2026-10-29T14:00:00Z",
	checks: 0,
	up: 0,
	degraded: 0,
	down: 0,
	maintenance: 0,
	avgLatencyMs: 0,
	maxLatencyMs: 0,
	...patch,
});

const checked = (patch: Partial<PointLike> = {}): PointLike =>
	point({ checks: 2, up: 2, avgLatencyMs: 40, maxLatencyMs: 60, ...patch });

describe("stateView", () => {
	test("each state has an icon, words and a tone", () => {
		expect(stateView("up")).toEqual({
			kind: "live",
			label: "Up",
			tone: "ok",
		});
		expect(stateView("degraded").kind).toBe("degraded");
		expect(stateView("down")).toEqual({
			kind: "failed",
			label: "Down",
			tone: "bad",
		});
		expect(stateView("maintenance").label).toBe("In maintenance");
		expect(stateView("paused")).toEqual({
			kind: "stopped",
			label: "Paused",
			tone: "paused",
		});
		expect(stateView("unknown").kind).toBe("unknown");
	});

	test("a state the server adds later reads as no data", () => {
		expect(stateView("sleeping")).toEqual(stateView("unknown"));
	});

	test("severity puts down and degraded first, paused last", () => {
		const order = ["up", "paused", "down", "maintenance", "unknown", "degraded"]
			.map((state) => ({ state, rank: stateSeverity(state) }))
			.sort((a, b) => a.rank - b.rank)
			.map((entry) => entry.state);
		expect(order).toEqual([
			"down",
			"degraded",
			"unknown",
			"maintenance",
			"up",
			"paused",
		]);
	});
});

describe("failingHint", () => {
	test("shows the count against the threshold while the state is still up", () => {
		expect(
			failingHint({
				state: "up",
				consecutiveFailures: 2,
				failureThreshold: 3,
			}),
		).toBe("Failing 2/3");
	});

	test("says nothing without failures or once the state moved on", () => {
		expect(
			failingHint({ state: "up", consecutiveFailures: 0, failureThreshold: 3 }),
		).toBeNull();
		expect(
			failingHint({
				state: "down",
				consecutiveFailures: 5,
				failureThreshold: 3,
			}),
		).toBeNull();
		expect(failingHint({ state: "up" })).toBeNull();
	});

	test("never reads above the threshold and copes without one", () => {
		expect(
			failingHint({
				state: "up",
				consecutiveFailures: 9,
				failureThreshold: 3,
			}),
		).toBe("Failing 3/3");
		expect(failingHint({ state: "up", consecutiveFailures: 2 })).toBe(
			"Failing 2",
		);
	});
});

describe("formatAvailability", () => {
	test("no data is not 0%", () => {
		expect(formatAvailability(null)).toBe("No data");
		expect(formatAvailability(undefined)).toBe("No data");
		expect(formatAvailability(0)).toBe("0%");
	});

	test("whole numbers drop the decimals", () => {
		expect(formatAvailability(100)).toBe("100%");
		expect(formatAvailability(95)).toBe("95%");
	});

	test("is floored, so downtime never reads 100%", () => {
		expect(formatAvailability(99.999)).toBe("99.99%");
		expect(formatAvailability(99.996)).toBe("99.99%");
		expect(formatAvailability(99.5)).toBe("99.5%");
		expect(formatAvailability(99.98)).toBe("99.98%");
	});

	test("stays within 0 to 100", () => {
		expect(formatAvailability(120)).toBe("100%");
		expect(formatAvailability(-3)).toBe("0%");
	});
});

describe("cellTone", () => {
	test("a bucket without checks is grey, or hatched while paused", () => {
		expect(cellTone(point(), "minute")).toBe("none");
		expect(cellTone(point(), "minute", true)).toBe("paused");
	});

	test("the worst state in a minute or hour wins", () => {
		expect(cellTone(checked(), "minute")).toBe("ok");
		expect(cellTone(checked({ checks: 2, up: 1, degraded: 1 }), "minute")).toBe(
			"warn",
		);
		expect(
			cellTone(checked({ checks: 2, up: 1, degraded: 0, down: 1 }), "hour"),
		).toBe("bad");
		expect(
			cellTone(checked({ checks: 3, up: 1, degraded: 1, down: 1 }), "minute"),
		).toBe("bad");
	});

	test("only maintenance is blue; maintenance beside passing checks is green", () => {
		expect(cellTone(point({ checks: 2, maintenance: 2 }), "minute")).toBe(
			"info",
		);
		expect(cellTone(point({ checks: 3, up: 1, maintenance: 2 }), "hour")).toBe(
			"ok",
		);
	});

	test("a UTC day is judged by its availability", () => {
		const day = (up: number, down: number) =>
			point({ checks: up + down, up, down });
		expect(cellTone(day(2877, 3), "day")).toBe("ok");
		expect(cellTone(day(2800, 80), "day")).toBe("warn");
		expect(cellTone(day(1000, 1880), "day")).toBe("bad");
		expect(cellTone(point(), "day")).toBe("none");
	});
});

describe("buildCells", () => {
	const minutes = (n: number): PointLike[] =>
		Array.from({ length: n }, (_, i) =>
			point({
				at: new Date(Date.UTC(2026, 9, 29, 14, i)).toISOString(),
				...(i === 7 ? { checks: 2, up: 1, down: 1 } : {}),
				...(i === 8 ? { checks: 2, up: 2 } : {}),
			}),
		);

	test("a 1h series is 60 minute cells, oldest first", () => {
		const cells = buildCells(minutes(60), "minute");
		expect(cells).toHaveLength(60);
		expect(cells[0]?.at).toBe("2026-10-29T14:00:00.000Z");
		expect(cells[59]?.at).toBe("2026-10-29T14:59:00.000Z");
		expect(cells[7]?.tone).toBe("bad");
		expect(cells[8]?.tone).toBe("ok");
		expect(cells[9]?.tone).toBe("none");
		expect(cells[7]?.end).toBe((cells[7]?.start ?? 0) + 60_000);
		expect(cells[7]?.availability).toBe(50);
		expect(cells[9]?.availability).toBeNull();
	});

	test("no series is no cells", () => {
		expect(buildCells(null, "minute")).toEqual([]);
		expect(buildCells(undefined, "hour")).toEqual([]);
	});

	test("empty buckets after the pause began are paused, earlier ones no data", () => {
		const cells = buildCells(minutes(10), "minute", "2026-10-29T14:05:30Z");
		expect(cells.map((cell) => cell.tone)).toEqual([
			"none",
			"none",
			"none",
			"none",
			"none",
			// 14:05 is still running when the pause began at 14:05:30.
			"paused",
			"paused",
			"bad",
			"ok",
			"paused",
		]);
	});

	test("the summary counts the checks and names the window", () => {
		const cells = buildCells(minutes(60), "minute");
		expect(stripSummary(cells, "1h")).toBe(
			"4 checks in the last hour: 3 up, 1 down",
		);
		expect(stripSummary([], "24h")).toBe("No checks in the last 24 hours");
	});

	test("the legend lists the tones present, worst first", () => {
		expect(tonesPresent(buildCells(minutes(60), "minute"))).toEqual([
			"bad",
			"ok",
			"none",
		]);
	});
});

describe("checksLine and cellTime", () => {
	test("a bucket's checks read as counts", () => {
		expect(
			checksLine({ checks: 0, up: 0, degraded: 0, down: 0, maintenance: 0 }),
		).toBe("No checks");
		expect(
			checksLine({
				checks: 2880,
				up: 2877,
				degraded: 0,
				down: 3,
				maintenance: 0,
			}),
		).toBe("2,880 checks: 2,877 up, 3 down");
		expect(
			checksLine({ checks: 1, up: 1, degraded: 0, down: 0, maintenance: 0 }),
		).toBe("1 check: 1 up");
	});

	test("times are UTC", () => {
		expect(cellTime("2026-10-29T14:03:00Z", "minute")).toBe(
			"2026-10-29 14:03 UTC",
		);
		expect(cellTime("2026-10-29T00:00:00Z", "day")).toBe("2026-10-29 UTC");
		expect(cellTime("nope", "hour")).toBe("");
	});
});

describe("cellError", () => {
	const cell = {
		start: Date.parse("2026-10-29T14:07:00Z"),
		end: Date.parse("2026-10-29T14:08:00Z"),
		down: 1,
	};
	const outage = {
		id: "o1",
		startedAt: "2026-10-29T14:05:00Z",
		resolvedAt: "2026-10-29T14:09:00Z",
		durationSeconds: 240,
		cause: "connection refused",
	};

	test("an overlapping outage explains the cell", () => {
		expect(cellError(cell, [outage], { at: null, error: "" })).toBe(
			"connection refused",
		);
	});

	test("the last error belongs to the cell holding the last check", () => {
		expect(
			cellError(cell, [], {
				at: "2026-10-29T14:07:30Z",
				error: "timeout after 5s",
			}),
		).toBe("timeout after 5s");
		expect(
			cellError(cell, [], { at: "2026-10-29T14:20:00Z", error: "timeout" }),
		).toBe("");
	});

	test("an outage that ended before the cell does not", () => {
		expect(
			cellError(
				cell,
				[
					{
						...outage,
						startedAt: "2026-10-29T13:00:00Z",
						resolvedAt: "2026-10-29T13:10:00Z",
					},
				],
				{ at: null, error: "" },
			),
		).toBe("");
	});
});

describe("formatSpan", () => {
	test("reads the largest units", () => {
		expect(formatSpan(42)).toBe("42s");
		expect(formatSpan(720)).toBe("12m");
		expect(formatSpan(3900)).toBe("1h 05m");
		expect(formatSpan(2 * 86_400 + 3 * 3_600)).toBe("2d 3h");
		expect(formatSpan(-5)).toBe("0s");
	});
});

describe("latencySummary", () => {
	test("weights the mean by passing checks and skips buckets without a figure", () => {
		const summary = latencySummary([
			checked({ checks: 1, up: 1, avgLatencyMs: 100, maxLatencyMs: 100 }),
			checked({ checks: 3, up: 3, avgLatencyMs: 20, maxLatencyMs: 300 }),
			point({ checks: 2, down: 2 }),
		]);
		expect(summary.avg).toBe(40);
		expect(summary.max).toBe(300);
	});

	test("no measurements is null, not 0", () => {
		expect(latencySummary([point({ checks: 1, down: 1 })])).toEqual({
			avg: null,
			max: null,
		});
		expect(latencySummary(null)).toEqual({ avg: null, max: null });
	});
});

describe("uptimeDays", () => {
	test("maps daily points to the days of the dither bar", () => {
		const days = uptimeDays([
			point({ at: "2026-10-27T00:00:00Z", checks: 2880, up: 2880 }),
			point({ at: "2026-10-28T00:00:00Z", checks: 2880, up: 2800, down: 80 }),
			point({ at: "2026-10-29T00:00:00Z", checks: 100, up: 40, down: 60 }),
			point({ at: "2026-10-30T00:00:00Z" }),
			point({ at: "2026-10-31T00:00:00Z", checks: 5, maintenance: 5 }),
		]);
		expect(days.map((day) => [day.date, day.status])).toEqual([
			["2026-10-27", "up"],
			["2026-10-28", "degraded"],
			["2026-10-29", "down"],
			["2026-10-30", "unknown"],
			["2026-10-31", "unknown"],
		]);
		expect(days[0]?.uptime).toBe(1);
		expect(days[3]?.uptime).toBeNull();
		expect(days[1]?.detail).toBe("2,880 checks: 2,800 up, 80 down");
		expect(days[3]?.detail).toBeUndefined();
		expect(days[4]?.detail).toBe("Maintenance only");
	});
});

describe("the environment overview", () => {
	const item = (name: string, state: string, monitored = true) => ({
		serviceId: name,
		name,
		state,
		monitored,
	});

	test("down and degraded sort first, unmonitored last, ties by name", () => {
		const sorted = sortServices([
			item("web", "up"),
			item("worker", "up", false),
			item("api", "down"),
			item("cache", "degraded"),
			item("db", "up"),
			item("batch", "paused"),
		]);
		expect(sorted.map((s) => s.name)).toEqual([
			"api",
			"cache",
			"db",
			"web",
			"batch",
			"worker",
		]);
		expect(sortServices(null)).toEqual([]);
	});

	test("counts what needs a look, ignoring unmonitored services", () => {
		expect(
			attentionCount([
				item("a", "down"),
				item("b", "degraded"),
				item("c", "down", false),
				item("d", "up"),
			]),
		).toBe(2);
	});

	test("switched-off monitoring reads as such, whatever the stored state", () => {
		expect(serviceStateView(item("a", "down"))).toEqual(stateView("down"));
		const off = serviceStateView(item("a", "up", false));
		expect(off.label).toBe("Monitoring off");
		expect(off.kind).toBe("stopped");
	});

	test("the note under a service: the error for down or degraded, a hint for up, else nothing", () => {
		const note = (state: string, lastError: string, monitored = true) =>
			rowNote({ ...item("a", state, monitored), lastError });
		expect(note("down", "HTTP 503")).toEqual({
			kind: "error",
			text: "HTTP 503",
		});
		expect(note("degraded", "1 of 2 replicas failing")?.kind).toBe("error");
		expect(note("up", "HTTP 503")).toEqual({ kind: "hint", text: "Failing" });
		expect(note("up", "")).toBeNull();
		expect(note("down", "")).toBeNull();
		expect(note("maintenance", "HTTP 503")).toBeNull();
		expect(note("paused", "HTTP 503")).toBeNull();
		expect(note("down", "HTTP 503", false)).toBeNull();
	});
});

describe("monitor form", () => {
	const saved = {
		enabled: true,
		alerts: true,
		failureThreshold: 3,
		recoveryThreshold: 2,
		reminderMinutes: 0,
	};

	test("starts from the saved monitor", () => {
		expect(toMonitorForm(saved)).toEqual({
			enabled: true,
			alerts: true,
			failureThreshold: "3",
			recoveryThreshold: "2",
			reminder: "off",
			reminderCustom: "",
		});
		expect(toMonitorForm({ ...saved, reminderMinutes: 60 }).reminder).toBe(
			"60",
		);
		const custom = toMonitorForm({ ...saved, reminderMinutes: 45 });
		expect(custom.reminder).toBe("custom");
		expect(custom.reminderCustom).toBe("45");
	});

	test("thresholds run from 1 to 20", () => {
		const form = toMonitorForm(saved);
		expect(validateMonitor(form)).toEqual({});
		expect(validateMonitor({ ...form, failureThreshold: "0" })).toHaveProperty(
			"failureThreshold",
		);
		expect(validateMonitor({ ...form, failureThreshold: "21" })).toHaveProperty(
			"failureThreshold",
		);
		expect(validateMonitor({ ...form, recoveryThreshold: "" })).toHaveProperty(
			"recoveryThreshold",
		);
		expect(
			validateMonitor({ ...form, recoveryThreshold: "2.5" }),
		).toHaveProperty("recoveryThreshold");
		expect(validateMonitor({ ...form, failureThreshold: "1" })).toEqual({});
		expect(validateMonitor({ ...form, recoveryThreshold: "20" })).toEqual({});
	});

	test("a custom reminder is 1 to 1440 minutes", () => {
		const form = { ...toMonitorForm(saved), reminder: "custom" as const };
		expect(validateMonitor({ ...form, reminderCustom: "" })).toHaveProperty(
			"reminderCustom",
		);
		expect(validateMonitor({ ...form, reminderCustom: "0" })).toHaveProperty(
			"reminderCustom",
		);
		expect(validateMonitor({ ...form, reminderCustom: "1441" })).toHaveProperty(
			"reminderCustom",
		);
		expect(validateMonitor({ ...form, reminderCustom: "1440" })).toEqual({});
		expect(
			buildMonitorBody({ ...form, reminderCustom: "90" })?.reminderMinutes,
		).toBe(90);
	});

	test("the body carries every field, with the reminder in minutes", () => {
		const form = toMonitorForm(saved);
		expect(buildMonitorBody(form)).toEqual(saved);
		expect(
			buildMonitorBody({ ...form, reminder: "30", alerts: false }),
		).toEqual({ ...saved, alerts: false, reminderMinutes: 30 });
		expect(buildMonitorBody({ ...form, failureThreshold: "99" })).toBeNull();
	});

	test("changed tells an edit from an untouched form", () => {
		const form = toMonitorForm(saved);
		expect(monitorChanged(form, saved)).toBe(false);
		expect(monitorChanged({ ...form, enabled: false }, saved)).toBe(true);
		expect(monitorChanged({ ...form, failureThreshold: "4" }, saved)).toBe(
			true,
		);
		expect(monitorChanged({ ...form, failureThreshold: "x" }, saved)).toBe(
			true,
		);
		// 3 and "03" are the same threshold.
		expect(monitorChanged({ ...form, failureThreshold: "03" }, saved)).toBe(
			false,
		);
	});

	test("explains thresholds in time and reminders in words", () => {
		expect(checksSpan(1)).toBe("30 s");
		expect(checksSpan(3)).toBe("1.5 min");
		expect(checksSpan(20)).toBe("10 min");
		expect(reminderText(0)).toBe("Off");
		expect(reminderText(30)).toBe("Every 30 min");
		expect(reminderText(120)).toBe("Every 2 h");
	});
});

describe("readoutRows", () => {
	const minute = (n: number, patch: Partial<PointLike> = {}) =>
		point({ at: `2026-10-29T14:0${n}:00Z`, ...patch });

	test("collapses a run of empty buckets into one row", () => {
		const cells = buildCells(
			[minute(0), minute(1), minute(2), minute(3, { checks: 2, up: 2 })],
			"minute",
		);
		const rows = readoutRows(cells);
		expect(rows).toHaveLength(2);
		const [run, busy] = rows;
		expect(run && "kind" in run ? run.count : 0).toBe(3);
		expect(busy && "kind" in busy).toBe(false);
	});

	test("keeps cells with checks on their own rows", () => {
		const cells = buildCells(
			[minute(0, { checks: 1, up: 1 }), minute(1, { checks: 1, up: 1 })],
			"minute",
		);
		expect(readoutRows(cells)).toHaveLength(2);
	});

	test("reads a run as a span and a duration", () => {
		const cells = buildCells([minute(0), minute(1), minute(2)], "minute");
		const [run] = readoutRows(cells);
		if (!run || !("kind" in run)) throw new Error("expected an empty run");
		expect(emptyRunTime(run, "minute")).toBe(
			"2026-10-29 14:00 UTC to 2026-10-29 14:02 UTC",
		);
		expect(emptyRunWords(run, "minute")).toBe("No checks for 3 minutes");
	});
});

describe("checksStoppedAt", () => {
	test("is the last check of a monitored service whose state is unknown", () => {
		expect(
			checksStoppedAt({
				state: "unknown",
				monitored: true,
				lastCheckAt: "2026-10-29T13:46:00Z",
			}),
		).toBe("2026-10-29T13:46:00Z");
	});

	test("is null while checks arrive, without a last check, or with monitoring off", () => {
		const at = "2026-10-29T13:46:00Z";
		expect(checksStoppedAt({ state: "up", lastCheckAt: at })).toBeNull();
		expect(checksStoppedAt({ state: "unknown", lastCheckAt: null })).toBeNull();
		expect(
			checksStoppedAt({ state: "unknown", monitored: false, lastCheckAt: at }),
		).toBeNull();
	});
});

describe("formatUtcStamp", () => {
	const now = Date.parse("2026-10-29T20:00:00Z");

	test("is the time alone on the current UTC day", () => {
		expect(formatUtcStamp("2026-10-29T13:46:12Z", now)).toBe("13:46 UTC");
	});

	test("adds the date on an earlier day", () => {
		expect(formatUtcStamp("2026-10-28T09:05:00Z", now)).toBe(
			"28 Oct 09:05 UTC",
		);
	});

	test("is empty for a value that is not a date", () => {
		expect(formatUtcStamp("later", now)).toBe("");
	});
});
