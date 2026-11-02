import { describe, expect, test } from "bun:test";
import type { MetricPoint, ServiceMetricPoint } from "#/api/gen/types.gen";
import {
	breakGaps,
	hasPeaks,
	isDelayed,
	latestReplicas,
	medianStepMs,
	processResources,
	REPLICA_COLORS,
	replicaBars,
	replicaColor,
	serviceMetricsParams,
	showReplicaList,
	toServerPoints,
	toServicePoints,
	visibleReplicas,
} from "./model";

const MB = 1024 * 1024;

const point = (over: Partial<ServiceMetricPoint> = {}): ServiceMetricPoint => ({
	ts: "2025-01-01T00:00:10Z",
	cpuPercent: 80,
	cpuPerReplica: 40,
	cpuMax: 95,
	memBytes: 400 * MB,
	memLimitBytes: 1024 * MB,
	memMax: 420 * MB,
	replicaCount: 2,
	...over,
});

describe("toServicePoints", () => {
	test("carries the aggregate and per-replica CPU and the replica count", () => {
		const [row] = toServicePoints([point()]);
		expect(row?.cpu).toBe(80);
		expect(row?.cpuPerReplica).toBe(40);
		expect(row?.replicaCount).toBe(2);
		expect(row?.memMb).toBe(400);
		expect(row?.limitMb).toBe(1024);
		expect(row?.memPct).toBeCloseTo((400 / 1024) * 100);
	});

	test("no limit means no percent", () => {
		const [row] = toServicePoints([point({ memLimitBytes: 0 })]);
		expect(row?.limitMb).toBeNull();
		expect(row?.memPct).toBeNull();
	});
});

describe("latestReplicas", () => {
	test("reads only the newest point, sorted by index, memory in MB", () => {
		const older = point({
			ts: "2025-01-01T00:00:00Z",
			replicas: [{ index: 0, cpuPercent: 1, memBytes: MB }],
		});
		const newest = point({
			replicas: [
				{ index: 1, cpuPercent: 50, memBytes: 300 * MB },
				{ index: 0, cpuPercent: 30, memBytes: 100 * MB },
			],
		});
		expect(latestReplicas([older, newest])).toEqual([
			{ index: 0, cpuPercent: 30, memMb: 100 },
			{ index: 1, cpuPercent: 50, memMb: 300 },
		]);
	});

	test("never invents replicas when the newest point has none", () => {
		const older = point({
			replicas: [{ index: 0, cpuPercent: 1, memBytes: MB }],
		});
		expect(latestReplicas([older, point()])).toEqual([]);
		expect(latestReplicas([point({ replicas: null })])).toEqual([]);
		expect(latestReplicas(undefined)).toEqual([]);
		expect(latestReplicas([])).toEqual([]);
	});
});

describe("replicaBars", () => {
	const replicas = [
		{ index: 0, cpuPercent: 50, memMb: 256 },
		{ index: 1, cpuPercent: 250, memMb: 600 },
	];

	test("fills are fractions of the cap and limit, never past full", () => {
		const [a, b] = replicaBars(replicas, 100, 512);
		expect(a?.cpuFrac).toBe(0.5);
		expect(a?.memFrac).toBe(0.5);
		expect(b?.cpuFrac).toBe(1);
		expect(b?.memFrac).toBe(1);
	});

	test("an unlimited memory has no fraction", () => {
		const [a] = replicaBars(replicas, 100, null);
		expect(a?.memFrac).toBeNull();
		expect(a?.memMb).toBe(256);
	});

	test("a zero cap draws empty bars instead of dividing by zero", () => {
		expect(replicaBars(replicas, 0, null).map((r) => r.cpuFrac)).toEqual([
			0, 0,
		]);
	});
});

describe("visibleReplicas", () => {
	const replicas = [{ index: 0 }, { index: 1 }, { index: 2 }];

	test("an empty pick is all of them", () => {
		expect(visibleReplicas(replicas, new Set())).toHaveLength(3);
	});

	test("a pick narrows the list", () => {
		expect(
			visibleReplicas(replicas, new Set([2, 0])).map((r) => r.index),
		).toEqual([0, 2]);
	});

	test("picks of replicas that are gone fall back to all", () => {
		expect(visibleReplicas(replicas, new Set([7]))).toHaveLength(3);
	});
});

describe("replicaColor", () => {
	test("is stable per index and wraps past the palette", () => {
		expect(replicaColor(0)).toBe(REPLICA_COLORS[0]);
		expect(replicaColor(REPLICA_COLORS.length)).toBe(REPLICA_COLORS[0]);
		expect(replicaColor(1)).not.toBe(replicaColor(0));
	});
});

describe("serviceMetricsParams", () => {
	test("asks for the service total without a process", () => {
		expect(serviceMetricsParams("1h")).toEqual({ range: "1h" });
		expect(serviceMetricsParams("1h", "")).toEqual({ range: "1h" });
	});

	test("narrows to one process when one is picked", () => {
		expect(serviceMetricsParams("6h", "worker")).toEqual({
			range: "6h",
			process: "worker",
		});
	});
});

describe("processResources", () => {
	const config = {
		memoryMb: 512,
		cpus: 1,
		processes: [
			{ name: "worker", memoryMb: 256 },
			{ name: "clock", cpus: 0.5 },
		],
	};

	test("a process uses its own limit and inherits the rest", () => {
		expect(processResources(config, "worker")).toEqual({
			memoryMb: 256,
			cpus: 1,
		});
		expect(processResources(config, "clock")).toEqual({
			memoryMb: 512,
			cpus: 0.5,
		});
	});

	test("the default process uses the service's limits", () => {
		expect(processResources(config, "web")).toEqual({
			memoryMb: 512,
			cpus: 1,
		});
	});

	test("the total of several processes assumes no single limit", () => {
		expect(processResources(config, undefined)).toEqual({
			memoryMb: undefined,
			cpus: undefined,
		});
	});

	test("a service without processes keeps its limits", () => {
		expect(
			processResources({ memoryMb: 128, processes: [] }, undefined),
		).toEqual({ memoryMb: 128, cpus: undefined });
	});
});

describe("showReplicaList", () => {
	test("lists replicas of one process, not of a mixed total", () => {
		expect(showReplicaList(true, undefined, 3)).toBe(false);
		expect(showReplicaList(true, "worker", 3)).toBe(true);
		expect(showReplicaList(false, undefined, 3)).toBe(true);
		expect(showReplicaList(true, "worker", 1)).toBe(false);
	});
});

const serverPoint = (over: Partial<MetricPoint> = {}): MetricPoint => ({
	ts: "2025-01-01T00:00:10Z",
	cpuPercent: 30,
	cpuMax: 55,
	diskPercent: 62,
	diskTotalBytes: 100 * MB,
	diskUsedBytes: 62 * MB,
	memBytes: 2048 * MB,
	memMax: 3072 * MB,
	memPercent: 25,
	memTotalBytes: 8192 * MB,
	...over,
});

describe("toServerPoints", () => {
	test("maps a sample to epoch ms and the percentages and byte totals the cards print", () => {
		expect(toServerPoints([serverPoint()])).toEqual([
			{
				t: Date.parse("2025-01-01T00:00:10Z"),
				cpu: 30,
				cpuMax: 55,
				memPct: 25,
				memMaxPct: (3072 / 8192) * 100,
				diskPct: 62,
				memUsed: 2048 * MB,
				memTotal: 8192 * MB,
				diskUsed: 62 * MB,
				diskTotal: 100 * MB,
			},
		]);
	});

	test("the peak falls back to the average when the sample was not downsampled", () => {
		const [row] = toServerPoints([
			serverPoint({
				cpuMax: undefined as unknown as number,
				memMax: undefined as unknown as number,
			}),
		]);
		expect(row?.cpuMax).toBe(30);
		expect(row?.memMaxPct).toBeCloseTo(25);
	});

	test("without a reported memory total there is no memory peak percent", () => {
		const [row] = toServerPoints([serverPoint({ memTotalBytes: 0 })]);
		expect(row?.memMaxPct).toBeNull();
	});

	test("null and empty responses give no rows", () => {
		expect(toServerPoints(null)).toEqual([]);
		expect(toServerPoints(undefined)).toEqual([]);
		expect(toServerPoints([])).toEqual([]);
	});
});

describe("toServicePoints fallbacks", () => {
	test("the peaks fall back to the averages", () => {
		const [row] = toServicePoints([
			point({
				cpuMax: undefined as unknown as number,
				memMax: undefined as unknown as number,
			}),
		]);
		expect(row?.cpuMax).toBe(80);
		expect(row?.memMaxMb).toBe(400);
	});

	test("memory percent never passes 100 when usage is over the limit", () => {
		const [row] = toServicePoints([
			point({ memBytes: 2048 * MB, memLimitBytes: 1024 * MB }),
		]);
		expect(row?.memPct).toBe(100);
	});

	test("null and undefined give no rows", () => {
		expect(toServicePoints(null)).toEqual([]);
		expect(toServicePoints(undefined)).toEqual([]);
	});
});

describe("hasPeaks", () => {
	test("true when some sample peaked clearly above its average", () => {
		expect(
			hasPeaks([
				{ cpu: 10, cpuMax: 10 },
				{ cpu: 10, cpuMax: 12 },
			]),
		).toBe(true);
	});

	test("rounding noise of 0.05 or less is not a peak", () => {
		expect(hasPeaks([{ cpu: 10, cpuMax: 10.05 }])).toBe(false);
		expect(hasPeaks([{ cpu: 10, cpuMax: 9 }])).toBe(false);
	});

	test("gap rows (null values) and no rows have no peaks", () => {
		expect(hasPeaks([{ cpu: null, cpuMax: null }])).toBe(false);
		expect(hasPeaks([{ cpu: 5, cpuMax: null }])).toBe(false);
		expect(hasPeaks([{ cpu: null, cpuMax: 5 }])).toBe(false);
		expect(hasPeaks([])).toBe(false);
	});
});

describe("breakGaps", () => {
	const blank = (t: number) => ({ t, v: null as number | null });
	const row = (t: number) => ({ t, v: 1 as number | null });

	test("inserts one blank row one step after the sample before an outage", () => {
		const out = breakGaps([row(0), row(10), row(100)], 10, blank);
		expect(out.map((r) => [r.t, r.v])).toEqual([
			[0, 1],
			[10, 1],
			[20, null],
			[100, 1],
		]);
	});

	test("a spacing of exactly three steps is still continuous", () => {
		const out = breakGaps([row(0), row(30)], 10, blank);
		expect(out.map((r) => r.t)).toEqual([0, 30]);
	});

	test("the factor sets how many steps count as an outage", () => {
		const out = breakGaps([row(0), row(30)], 10, blank, 2);
		expect(out.map((r) => r.t)).toEqual([0, 10, 30]);
	});

	test("fewer than two rows or a non-positive step pass through as a copy", () => {
		const one = [row(0)];
		const copy = breakGaps(one, 10, blank);
		expect(copy).toEqual(one);
		expect(copy).not.toBe(one);
		expect(breakGaps([row(0), row(1000)], 0, blank).map((r) => r.t)).toEqual([
			0, 1000,
		]);
		expect(breakGaps([], 10, blank)).toEqual([]);
	});
});

describe("medianStepMs", () => {
	test("is the median spacing, so one outage does not skew it", () => {
		expect(
			medianStepMs([{ t: 0 }, { t: 10 }, { t: 20 }, { t: 30 }, { t: 500 }]),
		).toBe(10);
	});

	test("zero below two rows", () => {
		expect(medianStepMs([])).toBe(0);
		expect(medianStepMs([{ t: 5 }])).toBe(0);
	});
});

describe("isDelayed", () => {
	test("true once the newest sample is older than three steps", () => {
		expect(isDelayed(1_000, 10, 1_031)).toBe(true);
	});

	test("exactly three steps old is still live", () => {
		expect(isDelayed(1_000, 10, 1_030)).toBe(false);
	});

	test("the factor is adjustable", () => {
		expect(isDelayed(1_000, 10, 1_030, 2)).toBe(true);
	});

	test("without a sample or a step there is nothing to call late", () => {
		expect(isDelayed(undefined, 10, 99_999)).toBe(false);
		expect(isDelayed(1_000, 0, 99_999)).toBe(false);
	});
});
