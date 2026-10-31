import { describe, expect, test } from "bun:test";
import {
	commandText,
	formatRunDuration,
	formatRunTimes,
	isActive,
	isValidTimezone,
	parseTimeoutSeconds,
	presetFor,
	rerunKind,
	runDurationMs,
	runPresentation,
	sourceLabel,
	splitCommand,
	timezoneOptions,
} from "./model";

describe("runPresentation", () => {
	test("every status carries a word next to its tone", () => {
		for (const status of [
			"queued",
			"running",
			"succeeded",
			"failed",
			"timed_out",
			"skipped",
		]) {
			expect(runPresentation(status).label.length).toBeGreaterThan(0);
		}
		expect(runPresentation("timed_out").label).toBe("Timed out");
	});
	test("only queued and running runs pulse", () => {
		expect(runPresentation("queued").pulse).toBe(true);
		expect(runPresentation("running").pulse).toBe(true);
		expect(runPresentation("failed").pulse).toBe(false);
	});
	test("an unknown status reads as written", () => {
		expect(runPresentation("weird")).toEqual({
			tone: "off",
			pulse: false,
			label: "weird",
		});
	});
});

test("isActive is true until a run finishes", () => {
	expect(isActive("queued")).toBe(true);
	expect(isActive("running")).toBe(true);
	for (const s of ["succeeded", "failed", "timed_out", "skipped"]) {
		expect(isActive(s)).toBe(false);
	}
});

describe("commandText", () => {
	test("quotes like the settings form and round-trips through splitCommand", () => {
		const argv = ["sh", "-c", 'echo "hi there"', ""];
		const text = commandText(argv);
		expect(text).toBe('sh -c "echo \\"hi there\\"" ""');
		expect(splitCommand(text)).toEqual(argv);
	});
	test("null and empty are an empty line", () => {
		expect(commandText(null)).toBe("");
		expect(commandText([])).toBe("");
	});
});

test("sourceLabel", () => {
	expect(sourceLabel("cron")).toBe("Scheduled");
	expect(sourceLabel("manual")).toBe("Run now");
	expect(sourceLabel("one_off")).toBe("Command");
	expect(sourceLabel("preview_seed")).toBe("Preview seed");
	expect(sourceLabel("other")).toBe("other");
});

describe("duration", () => {
	test("start to finish, or to now while running", () => {
		const run = {
			startedAt: "2026-01-01T00:00:00Z",
			finishedAt: "2026-01-01T00:00:10.600Z",
		};
		expect(runDurationMs(run)).toBe(10_600);
		expect(formatRunDuration(run)).toBe("10.6s");
		const running = { startedAt: "2026-01-01T00:00:00Z" };
		expect(formatRunDuration(running, Date.parse("2026-01-01T00:05:02Z"))).toBe(
			"5m 02s",
		);
	});
	test("a run that never started has no duration", () => {
		expect(runDurationMs({})).toBeNull();
		expect(formatRunDuration({})).toBe("–");
	});
});

describe("rerunKind", () => {
	test("task runs go through the task, one-offs through their command", () => {
		expect(rerunKind({ taskId: "t", source: "cron", command: ["a"] })).toBe(
			"task",
		);
		expect(rerunKind({ source: "one_off", command: ["a"] })).toBe("command");
		expect(rerunKind({ source: "one_off", command: null })).toBeNull();
		expect(rerunKind({ source: "preview_seed", command: ["a"] })).toBeNull();
	});
});

describe("formatRunTimes", () => {
	test("shows the schedule's zone, UTC and the reader's third zone", () => {
		// 03:00 Berlin (CET) is 02:00 UTC; Tokyo reads 11:00.
		expect(
			formatRunTimes("2026-10-27T02:00:00Z", "Europe/Berlin", "Asia/Tokyo"),
		).toEqual({
			zoned: "Tue 27 Oct 03:00",
			utc: "02:00 UTC",
			local: "Tue 27 Oct 11:00",
		});
	});
	test("the UTC date shows only when it differs from the schedule's", () => {
		expect(
			formatRunTimes("2026-10-27T23:30:00Z", "Asia/Tokyo", "Asia/Tokyo"),
		).toEqual({
			zoned: "Wed 28 Oct 08:30",
			utc: "Tue 27 Oct 23:30 UTC",
			local: null,
		});
	});
	test("the reader's zone is left out when it equals the schedule's or UTC", () => {
		expect(
			formatRunTimes("2026-10-27T02:00:00Z", "Europe/Berlin", "Europe/Berlin")
				.local,
		).toBeNull();
		expect(
			formatRunTimes("2026-10-27T02:00:00Z", "Europe/Berlin", "UTC").local,
		).toBeNull();
	});
	test("an empty zone is UTC, and a UTC schedule shows the reader's time", () => {
		expect(formatRunTimes("2026-10-27T00:00:00Z", "", "Europe/Berlin")).toEqual(
			{
				zoned: "Tue 27 Oct 00:00",
				utc: "00:00 UTC",
				local: "Tue 27 Oct 01:00",
			},
		);
	});
	test("daylight saving moves the same wall time to another UTC instant", () => {
		const summer = formatRunTimes(
			"2026-07-01T01:00:00Z",
			"Europe/Berlin",
			"UTC",
		);
		const winter = formatRunTimes(
			"2026-01-01T02:00:00Z",
			"Europe/Berlin",
			"UTC",
		);
		expect(summer.zoned).toBe("Wed 1 Jul 03:00");
		expect(winter.zoned).toBe("Thu 1 Jan 03:00");
	});
	test("a bad instant is empty", () => {
		expect(formatRunTimes("nope", "UTC", "UTC")).toEqual({
			zoned: "",
			utc: "",
			local: null,
		});
	});
});

describe("isValidTimezone", () => {
	test("accepts IANA names and the empty default, rejects the rest", () => {
		expect(isValidTimezone("")).toBe(true);
		expect(isValidTimezone("UTC")).toBe(true);
		expect(isValidTimezone("Europe/Berlin")).toBe(true);
		expect(isValidTimezone("Mars/Base")).toBe(false);
		expect(isValidTimezone("not a zone")).toBe(false);
	});
});

test("timezoneOptions lists UTC first without repeating it", () => {
	const zones = timezoneOptions();
	expect(zones[0]).toBe("UTC");
	expect(zones.filter((z) => z === "UTC")).toHaveLength(1);
});

test("presetFor matches presets exactly", () => {
	expect(presetFor("*/15 * * * *")).toBe("every15");
	expect(presetFor("@hourly")).toBe("hourly");
	expect(presetFor("0 3 * * *")).toBe("daily");
	expect(presetFor("0 3 * * 0")).toBe("weekly");
	expect(presetFor("0 5 * * *")).toBe("custom");
});

test("parseTimeoutSeconds accepts whole seconds of at least 1", () => {
	expect(parseTimeoutSeconds("1800")).toBe(1800);
	expect(parseTimeoutSeconds(" 30 ")).toBe(30);
	expect(parseTimeoutSeconds("0")).toBeNull();
	expect(parseTimeoutSeconds("1.5")).toBeNull();
	expect(parseTimeoutSeconds("")).toBeNull();
	expect(parseTimeoutSeconds("-3")).toBeNull();
});
