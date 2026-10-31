import { describe, expect, test } from "bun:test";
import type { ServiceMetricPoint } from "#/api/gen/types.gen";
import {
	latestReplicas,
	processResources,
	REPLICA_COLORS,
	replicaBars,
	replicaColor,
	serviceMetricsParams,
	showReplicaList,
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
