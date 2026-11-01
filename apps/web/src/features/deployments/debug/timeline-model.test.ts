import { describe, expect, test } from "bun:test";
import type { EventView } from "#/api/gen/types.gen";
import {
	buildTimeline,
	clockWithMs,
	formatOffset,
	summarizeTimeline,
} from "./timeline-model";

function event(
	id: number,
	createdAt: string,
	step = "pull",
	status = "started",
	message = "",
): EventView {
	return { id, createdAt, step, status, message };
}

const T0 = "2026-10-27T10:00:00.000Z";

describe("buildTimeline", () => {
	test("measures from the origin and from the previous event", () => {
		const rows = buildTimeline(
			[
				event(1, "2026-10-27T10:00:00.250Z", "status", "queued"),
				event(2, "2026-10-27T10:00:01.500Z", "pull", "started"),
				event(3, "2026-10-27T10:00:04.700Z", "pull", "succeeded"),
			],
			T0,
		);
		expect(rows.map((r) => r.sinceStart)).toEqual([250, 1_500, 4_700]);
		expect(rows.map((r) => r.sinceLast)).toEqual([null, 1_250, 3_200]);
	});

	test("orders by event id, not by the order given", () => {
		const rows = buildTimeline(
			[
				event(3, "2026-10-27T10:00:03.000Z"),
				event(1, "2026-10-27T10:00:01.000Z"),
				event(2, "2026-10-27T10:00:02.000Z"),
			],
			T0,
		);
		expect(rows.map((r) => r.id)).toEqual([1, 2, 3]);
		expect(rows.map((r) => r.sinceLast)).toEqual([null, 1_000, 1_000]);
	});

	test("defaults the origin to the first event", () => {
		const rows = buildTimeline([
			event(1, "2026-10-27T10:00:00.000Z"),
			event(2, "2026-10-27T10:00:00.084Z"),
		]);
		expect(rows.map((r) => r.sinceStart)).toEqual([0, 84]);
	});

	test("keeps the same-millisecond order and reports a zero gap", () => {
		const rows = buildTimeline(
			[
				event(1, "2026-10-27T10:00:01.000Z"),
				event(2, "2026-10-27T10:00:01.000Z"),
			],
			T0,
		);
		expect(rows.map((r) => r.sinceLast)).toEqual([null, 0]);
	});

	test("an unparsable time leaves its offsets empty and does not break the next gap", () => {
		const rows = buildTimeline(
			[
				event(1, "2026-10-27T10:00:01.000Z"),
				event(2, "not a time"),
				event(3, "2026-10-27T10:00:03.000Z"),
			],
			T0,
		);
		expect(rows[1]?.sinceStart).toBeNull();
		expect(rows[1]?.sinceLast).toBeNull();
		expect(rows[2]?.sinceLast).toBe(2_000);
	});

	test("an event before the origin is negative", () => {
		const rows = buildTimeline([event(1, "2026-10-27T09:59:59.900Z")], T0);
		expect(rows[0]?.sinceStart).toBe(-100);
	});

	test("an empty list is empty", () => {
		expect(buildTimeline([], T0)).toEqual([]);
	});
});

describe("summarizeTimeline", () => {
	test("totals and the slowest gap", () => {
		const rows = buildTimeline(
			[
				event(1, "2026-10-27T10:00:00.100Z"),
				event(2, "2026-10-27T10:00:00.200Z"),
				event(3, "2026-10-27T10:00:05.200Z"),
				event(4, "2026-10-27T10:00:05.500Z"),
			],
			T0,
		);
		expect(summarizeTimeline(rows)).toEqual({
			count: 4,
			totalMs: 5_500,
			slowest: { id: 3, ms: 5_000 },
		});
	});

	test("one event has no gap", () => {
		const rows = buildTimeline([event(1, "2026-10-27T10:00:00.100Z")], T0);
		expect(summarizeTimeline(rows)).toEqual({
			count: 1,
			totalMs: 100,
			slowest: null,
		});
	});

	test("nothing yet", () => {
		expect(summarizeTimeline([])).toEqual({
			count: 0,
			totalMs: null,
			slowest: null,
		});
	});
});

describe("formatOffset", () => {
	test("milliseconds under a second", () => {
		expect(formatOffset(0)).toBe("+0 ms");
		expect(formatOffset(84)).toBe("+84 ms");
		expect(formatOffset(999)).toBe("+999 ms");
	});

	test("seconds with three decimals", () => {
		expect(formatOffset(1_000)).toBe("+1.000 s");
		expect(formatOffset(1_204)).toBe("+1.204 s");
		expect(formatOffset(59_999)).toBe("+59.999 s");
	});

	test("minutes", () => {
		expect(formatOffset(64_200)).toBe("+1 m 04.200 s");
		expect(formatOffset(600_000)).toBe("+10 m 00.000 s");
	});

	test("a negative offset and a missing one", () => {
		expect(formatOffset(-12)).toBe("−12 ms");
		expect(formatOffset(null)).toBe("");
	});

	test("rounds fractional milliseconds", () => {
		expect(formatOffset(83.6)).toBe("+84 ms");
	});
});

describe("clockWithMs", () => {
	test("pads every field, milliseconds to three digits", () => {
		const iso = new Date(2026, 9, 27, 4, 5, 6, 7).toISOString();
		expect(clockWithMs(iso)).toBe("04:05:06.007");
	});

	test("empty for nothing or garbage", () => {
		expect(clockWithMs(null)).toBe("");
		expect(clockWithMs("nope")).toBe("");
	});
});
