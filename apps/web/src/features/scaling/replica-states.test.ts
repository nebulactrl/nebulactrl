import { describe, expect, test } from "bun:test";
import {
	groupProcessReplicas,
	groupReplicas,
	type ObservedReplica,
	PIP_LIMIT,
	type ReplicaStripInput,
} from "./replica-states";

const up = (index: number): ObservedReplica => ({
	index,
	state: "running",
	health: "healthy",
});
const starting = (index: number): ObservedReplica => ({
	index,
	state: "running",
	health: "starting",
});
const unhealthy = (index: number): ObservedReplica => ({
	index,
	state: "running",
	health: "unhealthy",
});
const exited = (index: number): ObservedReplica => ({
	index,
	state: "exited",
	health: "",
});

function input(
	wanted: number,
	replicas: ObservedReplica[] | null,
	overrides: Partial<ReplicaStripInput> = {},
): ReplicaStripInput {
	return { wanted, replicas, ready: 0, stopped: false, ...overrides };
}

const allUp = (count: number) =>
	Array.from({ length: count }, (_, index) => up(index));

describe("groupReplicas thresholds", () => {
	test("0 replicas: nothing to draw", () => {
		const strip = groupReplicas(input(0, []));
		expect(strip.total).toBe(0);
		expect(strip.mode).toBe("pips");
		expect(strip.pips).toEqual([]);
		expect(strip.legend).toEqual([]);
		expect(strip.summary).toBe("No replicas");
	});

	test("1 replica: one pip", () => {
		const strip = groupReplicas(input(1, allUp(1)));
		expect(strip.mode).toBe("pips");
		expect(strip.pips.map((pip) => pip.state)).toEqual(["ready"]);
		expect(strip.summary).toBe("1 of 1 replica ready");
		expect(strip.legend).toEqual([
			{ state: "ready", label: "Ready", count: 1 },
		]);
	});

	test("2 of 2 ready", () => {
		const strip = groupReplicas(input(2, allUp(2)));
		expect(strip.pips.map((pip) => pip.state)).toEqual(["ready", "ready"]);
		expect(strip.counts).toEqual({
			ready: 2,
			starting: 0,
			failed: 0,
			stopped: 0,
		});
		expect(strip.summary).toBe("2 of 2 replicas ready");
	});

	test("16 replicas is the last count with a pip each", () => {
		expect(PIP_LIMIT).toBe(16);
		const strip = groupReplicas(input(16, allUp(16)));
		expect(strip.mode).toBe("pips");
		expect(strip.pips).toHaveLength(16);
		expect(strip.summary).toBe("16 of 16 replicas ready");
	});

	test("17 replicas turn into one stacked bar", () => {
		const strip = groupReplicas(input(17, allUp(17)));
		expect(strip.mode).toBe("bar");
		expect(strip.pips).toEqual([]);
		expect(strip.counts.ready).toBe(17);
		expect(strip.summary).toBe("17 of 17 replicas ready");
	});

	test("40 replicas count every state without pips", () => {
		const replicas = [
			...Array.from({ length: 30 }, (_, index) => up(index)),
			...Array.from({ length: 6 }, (_, index) => starting(30 + index)),
			...Array.from({ length: 3 }, (_, index) => unhealthy(36 + index)),
		];
		const strip = groupReplicas(input(40, replicas));
		expect(strip.mode).toBe("bar");
		expect(strip.pips).toEqual([]);
		expect(strip.counts).toEqual({
			ready: 30,
			starting: 6,
			failed: 3,
			stopped: 1,
		});
		expect(strip.legend.map((item) => `${item.label} ${item.count}`)).toEqual([
			"Ready 30",
			"Starting 6",
			"Failed 3",
			"Not running 1",
		]);
		expect(strip.summary).toBe(
			"30 of 40 replicas ready, 6 starting, 3 failed, 1 not running",
		);
	});
});

describe("groupReplicas states", () => {
	test("mixed states are ordered ready, starting, failed, then by index", () => {
		const strip = groupReplicas(
			input(5, [unhealthy(0), up(1), starting(2), up(3), exited(4)]),
		);
		expect(strip.pips.map((pip) => [pip.index, pip.state])).toEqual([
			[1, "ready"],
			[3, "ready"],
			[2, "starting"],
			[0, "failed"],
			[4, "failed"],
		]);
		expect(strip.legend.map((item) => `${item.label} ${item.count}`)).toEqual([
			"Ready 2",
			"Starting 1",
			"Failed 2",
		]);
		expect(strip.summary).toBe("2 of 5 replicas ready, 1 starting, 2 failed");
	});

	test("the legend lists only states that occur", () => {
		const strip = groupReplicas(input(3, [up(0), up(1), up(2)]));
		expect(strip.legend.map((item) => item.state)).toEqual(["ready"]);
	});

	test("a wanted replica the agent never reported is not placed, drawn last", () => {
		const strip = groupReplicas(input(3, [up(0), starting(2)]));
		expect(strip.pips).toEqual([
			{ index: 0, state: "ready", detail: "Healthy" },
			{ index: 2, state: "starting", detail: "Waiting for its health check" },
			{ index: 1, state: "stopped", detail: "Not placed" },
		]);
	});

	test("replicas past the wanted count are being scaled in and have no slot", () => {
		const strip = groupReplicas(input(2, [up(0), up(1), up(2), unhealthy(3)]));
		expect(strip.total).toBe(2);
		expect(strip.counts).toEqual({
			ready: 2,
			starting: 0,
			failed: 0,
			stopped: 0,
		});
	});

	test("a stopped service shows every replica as not running, never failed", () => {
		const strip = groupReplicas(
			input(2, [exited(0), exited(1)], { stopped: true }),
		);
		expect(strip.pips.map((pip) => pip.state)).toEqual(["stopped", "stopped"]);
		expect(strip.summary).toBe("0 of 2 replicas ready, 2 not running");
	});

	test("without per-replica observation the ready count fills the front", () => {
		const strip = groupReplicas(input(3, null, { ready: 1 }));
		expect(strip.pips.map((pip) => pip.state)).toEqual([
			"ready",
			"stopped",
			"stopped",
		]);
		expect(groupReplicas(input(2, [], { ready: 5 })).counts.ready).toBe(2);
	});
});

describe("groupProcessReplicas", () => {
	const of = (process: string, replica: ObservedReplica): ObservedReplica => ({
		...replica,
		process,
	});
	const processes = [
		{ process: "web", isDefault: true, wanted: 2, ready: 0 },
		{ process: "worker", isDefault: false, wanted: 2, ready: 0 },
	];

	test("each process has its own slots, listed in process order", () => {
		const strip = groupProcessReplicas({
			processes,
			replicas: [
				of("worker", up(0)),
				of("worker", unhealthy(1)),
				of("web", up(0)),
				of("web", up(1)),
			],
			stopped: false,
		});
		expect(strip.total).toBe(4);
		expect(
			strip.pips.map((pip) => [pip.process, pip.index, pip.state]),
		).toEqual([
			["web", 0, "ready"],
			["web", 1, "ready"],
			["worker", 0, "ready"],
			["worker", 1, "failed"],
		]);
		expect(strip.summary).toBe("3 of 4 replicas ready, 1 failed");
	});

	test("a replica the agent reports with no process belongs to the default process", () => {
		const strip = groupProcessReplicas({
			processes,
			replicas: [up(0), up(1)],
			stopped: false,
		});
		expect(strip.counts.ready).toBe(2);
		expect(strip.pips.filter((pip) => pip.process === "worker")).toEqual([
			{ index: 0, state: "stopped", detail: "Not placed", process: "worker" },
			{ index: 1, state: "stopped", detail: "Not placed", process: "worker" },
		]);
	});

	test("a process with no observation falls back to its ready count", () => {
		const strip = groupProcessReplicas({
			processes: [{ process: "worker", isDefault: false, wanted: 3, ready: 2 }],
			replicas: null,
			stopped: false,
		});
		expect(strip.counts).toEqual({
			ready: 2,
			starting: 0,
			failed: 0,
			stopped: 1,
		});
	});

	test("only the processes given are drawn", () => {
		const strip = groupProcessReplicas({
			processes: [processes[1] as (typeof processes)[number]],
			replicas: [of("web", up(0)), of("worker", up(0)), of("worker", up(1))],
			stopped: false,
		});
		expect(strip.total).toBe(2);
		expect(strip.pips.every((pip) => pip.process === "worker")).toBe(true);
	});
});
