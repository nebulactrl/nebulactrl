import { describe, expect, test } from "bun:test";
import type {
	DrainView,
	FleetServer,
	Move,
	PlanMove,
} from "#/api/gen/types.gen";
import {
	canStartDrain,
	canSubmitDrain,
	deriveDrainProgress,
	drainBar,
	fleetServiceTone,
	isSchedulable,
	memoryMeter,
	moveAppearance,
	movesFromPlan,
	overcommitText,
	percentOf,
	planReadiness,
	serviceMemPercent,
	unschedulableReason,
	visibleFleetServers,
} from "./model";

const GIB = 1024 ** 3;

function move(over: Partial<Move>): Move {
	return {
		deploymentId: "",
		environmentName: "production",
		message: "",
		projectName: "Shop",
		serviceId: "s1",
		serviceName: "web",
		sourceServerId: "a",
		stateful: false,
		status: "pending",
		targetServerId: "b",
		targetServerName: "dst",
		...over,
	};
}

function drain(state: DrainView["state"], moves: Move[]): DrainView {
	return {
		actorLabel: "",
		cancelRequested: false,
		createdAt: "",
		finishedAt: null,
		id: "d",
		message: "",
		moves,
		serverId: "a",
		state,
	};
}

describe("capacity math", () => {
	test("percentOf is null without a total", () => {
		expect(percentOf(5, 0)).toBeNull();
		expect(percentOf(1, 4)).toBe(25);
	});

	test("memoryMeter marks committed limits and clamps the marker", () => {
		const m = memoryMeter({
			memUsedBytes: 1 * GIB,
			memTotalBytes: 4 * GIB,
			committedMemBytes: 2 * GIB,
		});
		expect(m.usedPercent).toBe(25);
		expect(m.committedPercent).toBe(50);
		expect(m.committedMarkerPercent).toBe(50);
		expect(m.overcommitted).toBe(false);
	});

	test("overcommit: committed above the host's memory", () => {
		const m = memoryMeter({
			memUsedBytes: 1 * GIB,
			memTotalBytes: 4 * GIB,
			committedMemBytes: 5 * GIB,
		});
		expect(m.overcommitted).toBe(true);
		expect(m.committedPercent).toBe(125);
		expect(m.committedMarkerPercent).toBe(100);
	});

	test("exactly the host's memory is not overcommit; unknown total never is", () => {
		expect(
			memoryMeter({
				memUsedBytes: 0,
				memTotalBytes: 4 * GIB,
				committedMemBytes: 4 * GIB,
			}).overcommitted,
		).toBe(false);
		const unknown = memoryMeter({
			memUsedBytes: 0,
			memTotalBytes: 0,
			committedMemBytes: 4 * GIB,
		});
		expect(unknown.overcommitted).toBe(false);
		expect(unknown.committedMarkerPercent).toBeNull();
		expect(unknown.usedPercent).toBeNull();
	});

	test("no limits means no marker", () => {
		expect(
			memoryMeter({
				memUsedBytes: 1,
				memTotalBytes: 10,
				committedMemBytes: 0,
			}).committedMarkerPercent,
		).toBeNull();
	});

	test("overcommitText names both amounts", () => {
		const text = overcommitText(
			{ memTotalBytes: 4 * GIB, committedMemBytes: 5 * GIB },
			(n) => `${n / GIB} GiB`,
		);
		expect(text).toContain("5 GiB");
		expect(text).toContain("1 GiB more");
	});

	test("a service's memory is measured against its own limit only", () => {
		expect(serviceMemPercent({ memBytes: 512, memLimitBytes: 1024 })).toBe(50);
		expect(serviceMemPercent({ memBytes: 512, memLimitBytes: 0 })).toBeNull();
	});
});

describe("schedulability", () => {
	test("draining and drained servers are closed, with a reason", () => {
		expect(isSchedulable({ status: "online", drainState: "none" })).toBe(true);
		expect(isSchedulable({ status: "online", drainState: "failed" })).toBe(
			true,
		);
		expect(isSchedulable({ status: "online", drainState: "draining" })).toBe(
			false,
		);
		expect(isSchedulable({ status: "online", drainState: "drained" })).toBe(
			false,
		);
		expect(
			unschedulableReason({ status: "online", drainState: "draining" }),
		).toBe("being drained");
		expect(
			unschedulableReason({ status: "online", drainState: "none" }),
		).toBeNull();
		expect(unschedulableReason({ status: "revoked", drainState: "none" })).toBe(
			"revoked",
		);
	});

	test("a server that does not accept workloads is closed, with a reason", () => {
		const off = {
			status: "online",
			drainState: "none",
			acceptWorkloads: false,
		} as const;
		expect(isSchedulable(off)).toBe(false);
		expect(unschedulableReason(off)).toBe("does not accept workloads");
		expect(isSchedulable({ ...off, acceptWorkloads: true })).toBe(true);
		// A drain is the more specific reason.
		expect(unschedulableReason({ ...off, drainState: "draining" })).toBe(
			"being drained",
		);
	});

	test("a build server says so rather than refusing workloads", () => {
		const builds = {
			status: "online",
			drainState: "none",
			acceptWorkloads: false,
			buildEnabled: true,
		} as const;
		expect(isSchedulable(builds)).toBe(false);
		expect(unschedulableReason(builds)).toBe("build server");
		// Builds and apps together stay open for placement.
		expect(isSchedulable({ ...builds, acceptWorkloads: true })).toBe(true);
	});

	test("a drain can start unless one runs or the server is unusable", () => {
		expect(canStartDrain({ status: "online", drainState: "none" })).toBe(true);
		expect(canStartDrain({ status: "offline", drainState: "failed" })).toBe(
			true,
		);
		expect(canStartDrain({ status: "online", drainState: "draining" })).toBe(
			false,
		);
		expect(canStartDrain({ status: "revoked", drainState: "none" })).toBe(
			false,
		);
	});
});

describe("drain progress", () => {
	test("counts finished moves and names the one in flight", () => {
		const p = deriveDrainProgress(
			drain("running", [
				move({ serviceName: "a", status: "done" }),
				move({ serviceName: "b", status: "deploying" }),
				move({ serviceName: "c", status: "pending" }),
			]),
		);
		expect(p).toMatchObject({
			total: 3,
			done: 1,
			activeIndex: 1,
			failedIndex: -1,
		});
		expect(p.percent).toBe(33);
		expect(p.label).toBe("Moving b… 1 of 3 done");
	});

	test("a failed move is named and nothing is active", () => {
		const p = deriveDrainProgress(
			drain("failed", [
				move({ serviceName: "a", status: "done" }),
				move({ serviceName: "b", status: "failed" }),
				move({ serviceName: "c", status: "pending" }),
			]),
		);
		expect(p.failedIndex).toBe(1);
		expect(p.label).toBe("Failed at b");
	});

	test("completed and canceled summaries", () => {
		expect(
			deriveDrainProgress(
				drain("completed", [
					move({ status: "done" }),
					move({ status: "done" }),
				]),
			),
		).toMatchObject({ percent: 100, label: "All 2 moved" });
		expect(
			deriveDrainProgress(
				drain("canceled", [
					move({ status: "done" }),
					move({ status: "pending" }),
				]),
			).label,
		).toBe("Canceled after 1 of 2");
	});

	test("no moves is complete", () => {
		expect(deriveDrainProgress(drain("completed", [])).percent).toBe(100);
	});

	test("the segmented bar has one block per move, the finished ones lit", () => {
		const p = deriveDrainProgress(
			drain("running", [
				move({ status: "done" }),
				move({ status: "done" }),
				move({ status: "deploying" }),
				move({ status: "pending" }),
			]),
		);
		expect(drainBar(p)).toEqual({ value: 2, max: 4, segments: 4 });
		expect(drainBar(deriveDrainProgress(drain("completed", [])))).toEqual({
			value: 1,
			max: 1,
			segments: null,
		});
	});

	test("move appearance reads in words", () => {
		expect(moveAppearance(move({ status: "pending" })).label).toBe("Waiting");
		expect(
			moveAppearance(
				move({ status: "deploying", deploymentStatus: "verifying" }),
			).label,
		).toBe("Deploying to dst (verifying)");
		expect(moveAppearance(move({ status: "done" })).tone).toBe("done");
		expect(moveAppearance(move({ status: "failed" })).tone).toBe("failed");
	});
});

describe("plan readiness", () => {
	const planMove = (over: Partial<PlanMove>): PlanMove => ({
		blocking: [],
		environmentName: "production",
		projectName: "Shop",
		serviceId: "s",
		serviceName: "web",
		stateful: false,
		targetServerId: "b",
		targetServerName: "dst",
		warnings: [],
		...over,
	});

	test("stateful services need the confirmation", () => {
		const r = planReadiness({
			capacity: { reasons: [], targetFits: true },
			moves: [planMove({}), planMove({ serviceName: "db", stateful: true })],
		});
		expect(r.statefulServices).toEqual(["db"]);
		expect(r.needsConfirmation).toBe(true);
		expect(canSubmitDrain(r, false)).toBe(false);
		expect(canSubmitDrain(r, true)).toBe(true);
	});

	test("blocking problems can never be confirmed away", () => {
		const r = planReadiness({
			capacity: { reasons: [], targetFits: true },
			moves: [
				planMove({
					blocking: [{ code: "no_target", message: "No online server" }],
					targetServerId: null as unknown as string,
				}),
			],
		});
		expect(r.blocking).toEqual(["No online server"]);
		expect(canSubmitDrain(r, true)).toBe(false);
	});

	test("an empty server can be drained without confirmation", () => {
		const r = planReadiness({
			capacity: { reasons: [], targetFits: true },
			moves: [],
		});
		expect(r.empty).toBe(true);
		expect(canSubmitDrain(r, false)).toBe(true);
	});

	test("the request body skips moves without a target", () => {
		expect(
			movesFromPlan([
				planMove({ serviceId: "x", targetServerId: "t" }),
				planMove({ serviceId: "y", targetServerId: null as unknown as string }),
			]),
		).toEqual([{ serviceId: "x", targetServerId: "t" }]);
	});
});

describe("service tones", () => {
	test("every status has a word and a status icon kind", () => {
		expect(fleetServiceTone("running")).toEqual({
			kind: "live",
			label: "Running",
		});
		expect(fleetServiceTone("deploying").kind).toBe("deploying");
		expect(fleetServiceTone("missing").kind).toBe("failed");
		expect(fleetServiceTone("undeployed").kind).toBe("pending");
		expect(fleetServiceTone("unknown").label).toBe("Unknown");
	});
});

describe("visible servers", () => {
	const server = (over: Partial<FleetServer>): FleetServer => ({
		committedMemBytes: 0,
		cpuPercent: 0,
		cpus: 0,
		diskTotalBytes: 0,
		diskUsedBytes: 0,
		drainMessage: "",
		drainState: "none",
		id: "x",
		memTotalBytes: 0,
		memUsedBytes: 0,
		name: "x",
		overcommitted: false,
		services: [],
		status: "online",
		unlimitedServices: 0,
		...over,
	});

	test("an empty revoked server is left out", () => {
		const list = [
			server({ name: "a" }),
			server({ name: "gone", status: "revoked" }),
		];
		expect(visibleFleetServers(list).map((s) => s.name)).toEqual(["a"]);
	});
});
