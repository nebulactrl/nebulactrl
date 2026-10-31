import { describe, expect, test } from "bun:test";
import { makeDeployment, makeObserved, makeService } from "./fixtures";
import {
	canCancel,
	cardStatus,
	formatElapsed,
	isInFlight,
	memoryFraction,
	memoryTone,
	nodeStatus,
	phaseOfStatus,
	phaseOfStep,
	phaseStates,
	sourceLabel,
} from "./status";

const OFFLINE = {
	id: "s",
	name: "hetzner-01",
	status: "offline",
	lastSeenAt: null,
} as const;

describe("nodeStatus", () => {
	test("no deployment reads Not deployed", () => {
		expect(nodeStatus(makeService()).label).toBe("Not deployed");
	});

	test("every deployment on its way is in flight", () => {
		for (const [status, label] of [
			["queued", "Queued"],
			["preparing", "Preparing"],
			["building", "Building"],
			["releasing", "Releasing"],
			["verifying", "Verifying"],
		] as const) {
			const service = makeService({
				latestDeployment: makeDeployment(status),
			});
			const s = nodeStatus(service);
			expect(s.label).toBe(label);
			expect(s.inFlight).toBe(true);
			expect(s.group).toBe("in_flight");
			expect(isInFlight(service)).toBe(true);
		}
	});

	test("an in-flight deploy on an offline server waits for it", () => {
		const service = makeService({
			server: OFFLINE,
			latestDeployment: makeDeployment("building"),
		});
		const s = nodeStatus(service);
		expect(s.label).toBe("Waiting for hetzner-01");
		expect(s.group).toBe("waiting");
		expect(s.inFlight).toBe(false);
		expect(isInFlight(service)).toBe(false);
	});

	test("failed with a serving release says so", () => {
		const serving = nodeStatus(
			makeService({
				currentDeploymentId: "dep-0",
				latestDeployment: makeDeployment("failed"),
			}),
		);
		expect(serving.label).toBe("Failed — previous release serving");
		expect(
			nodeStatus(makeService({ latestDeployment: makeDeployment("failed") }))
				.label,
		).toBe("Failed");
	});

	test("succeeded reads Deployed with its time and source", () => {
		const finishedAt = new Date(Date.now() - 2 * 60_000).toISOString();
		const s = nodeStatus(
			makeService({
				currentDeploymentId: "dep-1",
				latestDeployment: makeDeployment("succeeded", { finishedAt }),
			}),
		);
		expect(s.label).toBe("Deployed 2m ago via image");
		expect(s.kind).toBe("live");
	});

	test("git sources name their host", () => {
		expect(
			sourceLabel({
				sourceType: "git",
				config: {
					...makeService().config,
					gitUrl: "https://github.com/a/b.git",
				},
			}),
		).toBe("GitHub");
	});

	test("rolled back and rollback failed", () => {
		expect(
			nodeStatus(
				makeService({ latestDeployment: makeDeployment("rolled_back") }),
			).label,
		).toBe("Rolled back");
		expect(
			nodeStatus(
				makeService({ latestDeployment: makeDeployment("rollback_failed") }),
			).group,
		).toBe("failed");
	});

	test("a missing container overrides a succeeded deployment", () => {
		const s = nodeStatus(
			makeService({
				currentDeploymentId: "dep-1",
				latestDeployment: makeDeployment("succeeded"),
				observed: makeObserved({ state: "missing", health: "none" }),
			}),
		);
		expect(s.label).toBe("Container missing");
	});

	test("a stopped service reads Stopped", () => {
		const s = nodeStatus(
			makeService({
				desiredState: "stopped",
				currentDeploymentId: "dep-1",
				latestDeployment: makeDeployment("succeeded"),
			}),
		);
		expect(s.group).toBe("stopped");
		expect(s.label).toBe("Stopped");
	});
});

describe("phases", () => {
	test("statuses and steps map onto the three LEDs", () => {
		expect(phaseOfStatus("queued")).toBe("build");
		expect(phaseOfStatus("building")).toBe("build");
		expect(phaseOfStatus("releasing")).toBe("release");
		expect(phaseOfStatus("verifying")).toBe("verify");
		expect(phaseOfStatus("succeeded")).toBeNull();
		expect(phaseOfStep("pull")).toBe("build");
		expect(phaseOfStep("create")).toBe("release");
		expect(phaseOfStep("health")).toBe("verify");
		expect(phaseOfStep("route")).toBe("verify");
		expect(phaseOfStep("verify")).toBe("verify");
		expect(phaseOfStep("predeploy")).toBe("release");
		expect(phaseOfStep("nonsense")).toBeNull();
		expect(phaseOfStep(null)).toBeNull();
	});

	test("the phases before the current one are done, the ones after unlit", () => {
		expect(phaseStates("build", "live")).toEqual(["live", "off", "off"]);
		expect(phaseStates("release", "live")).toEqual(["done", "live", "off"]);
		expect(phaseStates("verify", "fail")).toEqual(["done", "done", "fail"]);
	});

	test("an in-flight deploy lights its phase and ticks a clock", () => {
		const createdAt = new Date(1_000_000).toISOString();
		const s = cardStatus(
			makeService({
				latestDeployment: makeDeployment("releasing", { createdAt }),
			}),
			1_000_000 + 42_000,
		);
		expect(s.pill).toBe("Deploying");
		expect(s.kind).toBe("deploying");
		expect(s.phase).toBe("release");
		expect(s.phases).toEqual(["done", "live", "off"]);
		expect(s.line).toBe("Releasing · 0:42");
		expect(s.inFlight).toBe(true);
		expect(s.clockFrom).toBe(createdAt);
	});

	test("building reads Building", () => {
		const s = cardStatus(
			makeService({ latestDeployment: makeDeployment("building") }),
			Date.parse("2026-01-01T00:01:05Z"),
		);
		expect(s.pill).toBe("Building");
		expect(s.kind).toBe("building");
		expect(s.phases).toEqual(["live", "off", "off"]);
		expect(s.line).toBe("Building · 1:05");
	});

	test("a failed deploy names the step it stopped at", () => {
		const s = cardStatus(
			makeService({
				latestDeployment: makeDeployment("failed", { failedStep: "health" }),
			}),
		);
		expect(s.pill).toBe("Failed");
		expect(s.phases).toEqual(["done", "done", "fail"]);
		expect(s.line).toBe("Failed at Verify");
		expect(s.lineTone).toBe("bad");
		expect(s.inFlight).toBe(false);
		expect(
			cardStatus(
				makeService({
					latestDeployment: makeDeployment("failed", { failedStep: "build" }),
				}),
			).phases,
		).toEqual(["fail", "off", "off"]);
		expect(
			cardStatus(makeService({ latestDeployment: makeDeployment("failed") }))
				.line,
		).toBe("Failed");
	});

	test("the pre-deploy step keeps the Release LED lit and flags the card", () => {
		const running = cardStatus(
			makeService({
				latestDeployment: makeDeployment("releasing", {
					currentStep: "predeploy",
				}),
			}),
		);
		expect(running.preDeploy).toBe(true);
		expect(running.phase).toBe("release");
		expect(running.phases).toEqual(["done", "live", "off"]);
		const other = cardStatus(
			makeService({
				latestDeployment: makeDeployment("releasing", { currentStep: "start" }),
			}),
		);
		expect(other.preDeploy).toBe(false);
		const failed = cardStatus(
			makeService({
				latestDeployment: makeDeployment("failed", { failedStep: "predeploy" }),
			}),
		);
		expect(failed.preDeploy).toBe(false);
		expect(failed.phases).toEqual(["done", "fail", "off"]);
		expect(failed.line).toBe("Failed at Pre-deploy");
	});

	test("a live service shows three done LEDs and its age", () => {
		const finishedAt = new Date(Date.now() - 2 * 3_600_000).toISOString();
		const s = cardStatus(
			makeService({
				currentDeploymentId: "dep-1",
				latestDeployment: makeDeployment("succeeded", { finishedAt }),
				observed: makeObserved(),
			}),
		);
		expect(s.pill).toBe("Live");
		expect(s.kind).toBe("live");
		expect(s.phases).toEqual(["done", "done", "done"]);
		expect(s.line).toBe("Live · 2h ago");
		expect(s.lineTone).toBe("ok");
	});

	test("replicas below desired read Degraded", () => {
		const s = cardStatus(
			makeService({
				currentDeploymentId: "dep-1",
				latestDeployment: makeDeployment("succeeded"),
				observed: makeObserved({
					replicasDesired: 2,
					replicasReady: 1,
					health: "degraded",
				}),
			}),
		);
		expect(s.pill).toBe("Degraded");
		expect(s.line).toBe("1/2 replicas ready");
	});

	test("stopped, offline and not deployed", () => {
		const stopped = cardStatus(
			makeService({
				desiredState: "stopped",
				currentDeploymentId: "dep-1",
				latestDeployment: makeDeployment("succeeded"),
			}),
		);
		expect(stopped.pill).toBe("Stopped");
		expect(stopped.phases).toEqual(["off", "off", "off"]);
		expect(stopped.line).toBe("Stopped");

		const offline = cardStatus(
			makeService({
				server: OFFLINE,
				currentDeploymentId: "dep-1",
				latestDeployment: makeDeployment("succeeded"),
			}),
		);
		expect(offline.pill).toBe("Offline");
		expect(offline.kind).toBe("offline");

		const waiting = cardStatus(
			makeService({
				server: OFFLINE,
				latestDeployment: makeDeployment("building"),
			}),
		);
		expect(waiting.pill).toBe("Offline");
		expect(waiting.line).toBe("Waiting for hetzner-01");
		expect(waiting.inFlight).toBe(false);

		const fresh = cardStatus(makeService());
		expect(fresh.pill).toBe("Not deployed");
		expect(fresh.phases).toEqual(["off", "off", "off"]);
	});

	test("a missing container fails the verify LED", () => {
		const s = cardStatus(
			makeService({
				currentDeploymentId: "dep-1",
				latestDeployment: makeDeployment("succeeded"),
				observed: makeObserved({ state: "missing", health: "none" }),
			}),
		);
		expect(s.pill).toBe("Failed");
		expect(s.line).toBe("Container missing");
	});

	test("rolled back is degraded at the step that failed", () => {
		const s = cardStatus(
			makeService({
				currentDeploymentId: "dep-0",
				latestDeployment: makeDeployment("rolled_back", {
					failedStep: "verify",
				}),
			}),
		);
		expect(s.pill).toBe("Degraded");
		expect(s.phases).toEqual(["done", "done", "fail"]);
	});
});

describe("helpers", () => {
	test("formatElapsed", () => {
		expect(formatElapsed(0)).toBe("0:00");
		expect(formatElapsed(42_000)).toBe("0:42");
		expect(formatElapsed(12 * 60_000 + 5_000)).toBe("12:05");
		expect(formatElapsed(3_723_000)).toBe("1:02:03");
		expect(formatElapsed(-5)).toBe("0:00");
	});

	test("a deployment can be canceled at any step until it is live", () => {
		for (const status of [
			"created",
			"queued",
			"preparing",
			"building",
			"releasing",
			"verifying",
		] as const) {
			expect(
				canCancel(makeService({ latestDeployment: makeDeployment(status) })),
			).toBe(true);
		}
		for (const status of ["succeeded", "failed", "canceled"] as const) {
			expect(
				canCancel(makeService({ latestDeployment: makeDeployment(status) })),
			).toBe(false);
		}
		expect(canCancel(makeService())).toBe(false);
	});

	test("memory share and tone", () => {
		expect(memoryFraction(194, 512)).toBeCloseTo(0.379, 2);
		expect(memoryFraction(600, 512)).toBe(1);
		expect(memoryFraction(10, 0)).toBeNull();
		expect(memoryFraction(Number.NaN, 512)).toBeNull();
		expect(memoryTone(0.5)).toBe("ok");
		expect(memoryTone(0.76)).toBe("warn");
		expect(memoryTone(0.93)).toBe("bad");
	});
});
