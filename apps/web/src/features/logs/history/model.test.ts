import { describe, expect, test } from "bun:test";
import type {
	LogHistoryEntry,
	LogHistoryPage,
	LogHistoryUnavailable,
} from "#/api/gen/types.gen";
import {
	continuation,
	customRangeProblem,
	emptyKind,
	epochOf,
	failureOf,
	footerText,
	formatStamp,
	fromLocalInput,
	HISTORY_MAX_LINES,
	joinNames,
	lowerBound,
	mergePages,
	offlineSentence,
	parseRange,
	RANGE_PRESETS,
	rangeLabel,
	rangeParams,
	reachesNow,
	resolveRange,
	summarizeUnavailable,
	toLocalInput,
	toLogLine,
} from "./model";

function entry(
	n: number,
	extra: Partial<LogHistoryEntry> = {},
): LogHistoryEntry {
	return {
		ts: new Date(Date.UTC(2026, 9, 20, 12, 0, n)).toISOString(),
		line: `line ${n}`,
		serviceId: "svc-1",
		replica: 0,
		stream: "stdout",
		...extra,
	};
}

/** A page as the server sends it: newest line first. */
function page(
	numbers: number[],
	extra: Partial<LogHistoryPage> = {},
): LogHistoryPage {
	return {
		capturing: true,
		items: numbers.map((n) => entry(n)),
		...extra,
	};
}

describe("range in the address", () => {
	test("an empty address is the last hour", () => {
		expect(parseRange({})).toEqual({ kind: "preset", id: "1h" });
	});

	test("a known preset is kept and an unknown one is dropped", () => {
		expect(parseRange({ range: "7d" })).toEqual({ kind: "preset", id: "7d" });
		expect(parseRange({ range: "90d" })).toEqual({ kind: "preset", id: "1h" });
		expect(parseRange({ range: 3 })).toEqual({ kind: "preset", id: "1h" });
	});

	test("from and to make a custom range and win over range", () => {
		expect(
			parseRange({
				range: "6h",
				from: "2026-10-20T10:00:00Z",
				to: "2026-10-20T11:00:00Z",
			}),
		).toEqual({
			kind: "custom",
			from: "2026-10-20T10:00:00.000Z",
			to: "2026-10-20T11:00:00.000Z",
		});
	});

	test("a custom range may be open at the end", () => {
		expect(parseRange({ from: "2026-10-20T10:00:00Z" })).toEqual({
			kind: "custom",
			from: "2026-10-20T10:00:00.000Z",
			to: undefined,
		});
	});

	test("an unreadable date is dropped, a reversed range loses its end", () => {
		expect(parseRange({ from: "yesterday" })).toEqual({
			kind: "preset",
			id: "1h",
		});
		expect(
			parseRange({
				from: "2026-10-20T12:00:00Z",
				to: "2026-10-20T11:00:00Z",
			}),
		).toEqual({
			kind: "custom",
			from: "2026-10-20T12:00:00.000Z",
			to: undefined,
		});
	});

	test("rangeParams writes the keys the other form removes", () => {
		expect(rangeParams({ kind: "preset", id: "6h" })).toEqual({
			range: "6h",
			from: undefined,
			to: undefined,
		});
		expect(
			rangeParams({
				kind: "custom",
				from: "2026-10-20T10:00:00.000Z",
				to: undefined,
			}),
		).toEqual({
			range: undefined,
			from: "2026-10-20T10:00:00.000Z",
			to: undefined,
		});
	});
});

describe("resolving a range", () => {
	const now = Date.UTC(2026, 9, 20, 12, 0, 0);

	test("a preset counts back from the moment it was asked for", () => {
		for (const preset of RANGE_PRESETS) {
			const range = resolveRange({ kind: "preset", id: preset.id }, now);
			expect(range.to).toBe(new Date(now).toISOString());
			expect(Date.parse(range.to) - Date.parse(range.from ?? "")).toBe(
				preset.ms,
			);
		}
	});

	test("a custom range keeps its ends; a missing end is the moment asked", () => {
		expect(
			resolveRange(
				{
					kind: "custom",
					from: "2026-10-20T10:00:00.000Z",
					to: "2026-10-20T11:00:00.000Z",
				},
				now,
			),
		).toEqual({
			from: "2026-10-20T10:00:00.000Z",
			to: "2026-10-20T11:00:00.000Z",
		});
		expect(
			resolveRange({ kind: "custom", from: undefined, to: undefined }, now),
		).toEqual({ from: undefined, to: new Date(now).toISOString() });
	});

	test("only a range that reaches now has anything new to refresh", () => {
		expect(reachesNow({ kind: "preset", id: "15m" })).toBe(true);
		expect(
			reachesNow({
				kind: "custom",
				from: "2026-10-20T10:00:00.000Z",
				to: undefined,
			}),
		).toBe(true);
		expect(
			reachesNow({
				kind: "custom",
				from: "2026-10-20T10:00:00.000Z",
				to: "2026-10-20T11:00:00.000Z",
			}),
		).toBe(false);
	});
});

describe("range text and fields", () => {
	const utc = { locale: "en-GB", timeZone: "UTC" };

	test("formatStamp is day, month and 24-hour time", () => {
		expect(formatStamp("2026-10-20T14:02:11Z", utc)).toBe("20 Oct, 14:02");
		expect(formatStamp("not a date", utc)).toBe("not a date");
	});

	test("formatStamp defaults to English, never the browser's locale", () => {
		expect(formatStamp("2026-10-27T07:22:00Z", { timeZone: "UTC" })).toBe(
			"27 Oct, 07:22",
		);
	});

	test("rangeLabel names a preset or the ends of a custom range", () => {
		expect(rangeLabel({ kind: "preset", id: "24h" }, utc)).toBe(
			"Last 24 hours",
		);
		expect(
			rangeLabel(
				{
					kind: "custom",
					from: "2026-10-20T10:00:00Z",
					to: "2026-10-20T11:30:00Z",
				},
				utc,
			),
		).toBe("20 Oct, 10:00 to 20 Oct, 11:30");
		expect(
			rangeLabel(
				{ kind: "custom", from: "2026-10-20T10:00:00Z", to: undefined },
				utc,
			),
		).toBe("Since 20 Oct, 10:00");
		expect(
			rangeLabel(
				{ kind: "custom", from: undefined, to: "2026-10-20T10:00:00Z" },
				utc,
			),
		).toBe("Until 20 Oct, 10:00");
	});

	test("datetime-local values are the reader's wall clock", () => {
		const iso = new Date(2026, 9, 20, 14, 2).toISOString();
		expect(toLocalInput(iso)).toBe("2026-10-20T14:02");
		expect(fromLocalInput("2026-10-20T14:02")).toBe(iso);
		expect(fromLocalInput("")).toBeUndefined();
		expect(fromLocalInput("garbage")).toBeUndefined();
		expect(toLocalInput(undefined)).toBe("");
	});

	test("a custom range needs an end that follows its start", () => {
		expect(customRangeProblem(undefined, undefined)).toBe(
			"Pick a start or an end.",
		);
		expect(
			customRangeProblem(
				"2026-10-20T12:00:00.000Z",
				"2026-10-20T11:00:00.000Z",
			),
		).toBe("The end must be after the start.");
		expect(
			customRangeProblem("2026-10-20T10:00:00.000Z", undefined),
		).toBeNull();
		expect(
			customRangeProblem(
				"2026-10-20T10:00:00.000Z",
				"2026-10-20T11:00:00.000Z",
			),
		).toBeNull();
	});
});

describe("pages and lines", () => {
	test("an entry becomes the line the view draws, with replica and deployment", () => {
		const line = toLogLine(
			entry(3, { deploymentId: "dep-9", serviceName: "api", replica: 2 }),
			7,
		);
		expect(line).toEqual({
			id: 7,
			ts: entry(3).ts,
			line: "line 3",
			serviceId: "svc-1",
			serviceName: "api",
			stream: "stdout",
			replica: 2,
			deploymentId: "dep-9",
		});
	});

	test("one page is reversed to oldest first", () => {
		const lines = mergePages([page([5, 4, 3])]);
		expect(lines.map((l) => l.line)).toEqual(["line 3", "line 4", "line 5"]);
		const ids = lines.map((l) => l.id);
		expect(ids).toEqual([...ids].sort((a, b) => a - b));
	});

	test("an older page goes in front and no line changes its id", () => {
		const first = mergePages([page([5, 4, 3])]);
		const both = mergePages([page([5, 4, 3]), page([2, 1])]);
		expect(both.map((l) => l.line)).toEqual([
			"line 1",
			"line 2",
			"line 3",
			"line 4",
			"line 5",
		]);
		expect(both.slice(2)).toEqual(first);
		const ids = both.map((l) => l.id);
		expect(new Set(ids).size).toBe(ids.length);
		expect(ids).toEqual([...ids].sort((a, b) => a - b));
	});

	test("pages without items give no lines", () => {
		expect(mergePages([])).toEqual([]);
		expect(mergePages([{ capturing: true, items: null }])).toEqual([]);
	});

	test("a new result never reuses an id of an earlier one", () => {
		const a = page([3, 2, 1]);
		const b = page([2, 1]);
		expect(epochOf(a)).toBe(epochOf(a));
		expect(epochOf(b)).toBeGreaterThan(epochOf(a));
		const first = mergePages([a], epochOf(a));
		const second = mergePages([b], epochOf(b));
		const lastOfFirst = first[first.length - 1]?.id ?? 0;
		expect(second[0]?.id ?? 0).toBeGreaterThan(lastOfFirst);
	});

	test("lowerBound finds the first line at or after an id", () => {
		const lines = [{ id: 10 }, { id: 20 }, { id: 30 }];
		expect(lowerBound(lines, 5)).toBe(0);
		expect(lowerBound(lines, 20)).toBe(1);
		expect(lowerBound(lines, 21)).toBe(2);
		expect(lowerBound(lines, 99)).toBe(3);
		expect(lowerBound([], 1)).toBe(0);
	});

	test("what the oldest page says decides whether there is more", () => {
		expect(continuation([page([1])], 1)).toEqual({ kind: "end" });
		expect(continuation([page([1], { nextCursor: "c1" })], 1)).toEqual({
			kind: "more",
			cursor: "c1",
		});
		expect(
			continuation(
				[
					page([2], { nextCursor: "c1" }),
					page([1], { nextCursor: "c2", partial: true }),
				],
				2,
			),
		).toEqual({ kind: "partial", cursor: "c2" });
		expect(
			continuation([page([1], { nextCursor: "c1" })], HISTORY_MAX_LINES),
		).toEqual({
			kind: "full",
		});
		expect(continuation([], 0)).toEqual({ kind: "end" });
	});
});

describe("states", () => {
	test("a 503 or 409 from the service endpoint names the problem", () => {
		expect(
			failureOf({
				status: 503,
				code: "server_offline",
				message: "hetzner-01 is offline",
			}),
		).toEqual({
			kind: "server_offline",
			message: "hetzner-01 is offline",
		});
		expect(
			failureOf({ status: 409, code: "agent_outdated", message: "old agent" })
				.kind,
		).toBe("agent_outdated");
		expect(failureOf({ status: 500, code: "x", message: "boom" })).toEqual({
			kind: "error",
			message: "boom",
		});
		expect(failureOf(null)).toEqual({
			kind: "error",
			message: "The history could not be read.",
		});
	});

	test("names are joined the way a sentence reads", () => {
		expect(joinNames([])).toBe("");
		expect(joinNames(["a"])).toBe("a");
		expect(joinNames(["a", "b"])).toBe("a and b");
		expect(joinNames(["a", "b", "c"])).toBe("a, b and c");
	});

	test("the offline sentence names the server", () => {
		expect(offlineSentence(["hetzner-01"])).toBe(
			"History is stored on hetzner-01 and is unavailable while it is offline.",
		);
		expect(offlineSentence(["a", "b"])).toBe(
			"History is stored on a and b and is unavailable while they are offline.",
		);
		expect(offlineSentence([])).toContain("unavailable while it is offline");
	});

	const offline = (
		serviceName: string,
		serverName: string,
	): LogHistoryUnavailable => ({
		serviceId: `id-${serviceName}`,
		serviceName,
		serverId: `srv-${serverName}`,
		serverName,
		reason: "server_offline",
		message: `${serverName} is offline`,
	});

	test("an environment lists each service whose server could not answer", () => {
		expect(summarizeUnavailable(null)).toBeNull();
		expect(summarizeUnavailable([])).toBeNull();
		const summary = summarizeUnavailable([
			offline("api", "hetzner-01"),
			offline("worker", "hetzner-01"),
		]);
		expect(summary?.title).toBe(
			"History is stored on hetzner-01 and is unavailable while it is offline.",
		);
		expect(summary?.items.map((i) => i.serviceName)).toEqual(["api", "worker"]);
		expect(summary?.items[0]?.message).toBe("hetzner-01 is offline");
	});

	test("mixed reasons fall back to a count", () => {
		const summary = summarizeUnavailable([
			offline("api", "a"),
			{ ...offline("web", "b"), reason: "failed", message: "disk error" },
		]);
		expect(summary?.title).toBe("History could not be read for 2 services.");
		const old = summarizeUnavailable([
			{ ...offline("api", "a"), reason: "agent_outdated" },
		]);
		expect(old?.title).toContain("too old");
	});

	test("an empty result is about the range or about the search", () => {
		expect(emptyKind("")).toBe("none-in-range");
		expect(emptyKind("   ")).toBe("none-in-range");
		expect(emptyKind("error")).toBe("no-match");
	});

	test("the footer says how far back the log goes", () => {
		const stamp = { locale: "en-GB", timeZone: "UTC" };
		expect(
			footerText({
				lines: 500,
				oldest: "2026-10-20T14:02:00Z",
				scope: "service",
				stamp,
				continuation: "more",
			}),
		).toBe("Showing 500 lines. This server keeps logs back to 20 Oct, 14:02.");
		expect(
			footerText({
				lines: 1,
				oldest: undefined,
				scope: "environment",
				continuation: "end",
			}),
		).toBe("Showing 1 line.");
		expect(
			footerText({
				lines: 2,
				oldest: "2026-10-20T14:02:00Z",
				scope: "environment",
				stamp,
				continuation: "end",
			}),
		).toContain("The servers keep logs back to");
		expect(
			footerText({
				lines: HISTORY_MAX_LINES,
				oldest: undefined,
				scope: "service",
				continuation: "full",
			}),
		).toContain("Narrow the range");
	});
});
