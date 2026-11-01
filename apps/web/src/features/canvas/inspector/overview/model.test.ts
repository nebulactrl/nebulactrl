import { describe, expect, test } from "bun:test";
import type { Service } from "#/api/models";
import {
	autoscalingLine,
	cpuFigure,
	formatUptime,
	healthVerdict,
	liveUptimeError,
	logLevel,
	memoryFigure,
	probeDescription,
	replicaCounts,
	restartTotal,
	scaleEventLine,
	uptimeMs,
} from "./model";

function service(overrides: Partial<Service> = {}): Service {
	return {
		id: "s1",
		name: "api",
		slug: "api",
		desiredState: "running",
		currentDeploymentId: "d1",
		latestDeployment: null,
		server: { id: "h", name: "agent1", status: "online", lastSeenAt: null },
		config: {
			port: 8080,
			cpus: 0,
			memoryMb: 0,
			healthcheck: {
				type: "http",
				path: "/healthz",
				port: 0,
				intervalSeconds: 2,
			},
		},
		scaling: {
			replicas: 2,
			ready: 1,
			autoscaling: { enabled: false, min: 1, max: 3, targetCpuPercent: 70 },
			lastScaleEvent: null,
			blockedReason: null,
			eligible: true,
			ineligibleReason: null,
		},
		observed: {
			containerId: "c",
			health: "healthy",
			imageId: "i",
			replicas: [
				{
					containerId: "a",
					health: "healthy",
					index: 0,
					restartCount: 1,
					startedAt: "2026-10-26T10:00:00Z",
					state: "running",
				},
				{
					containerId: "b",
					health: "healthy",
					index: 1,
					restartCount: 2,
					startedAt: "2026-10-26T10:00:00Z",
					state: "running",
				},
			],
			replicasDesired: 2,
			replicasReady: 2,
			startedAt: "2026-10-26T10:00:00Z",
			state: "running",
		},
		...overrides,
	} as unknown as Service;
}

describe("healthVerdict", () => {
	test("healthy names the probe", () => {
		const verdict = healthVerdict(service());
		expect(verdict.label).toBe("Healthy");
		expect(verdict.kind).toBe("healthy");
		expect(verdict.detail).toBe("HTTP GET /healthz on :8080 · every 2s");
	});
	test("degraded counts replicas", () => {
		const s = service();
		if (s.observed) {
			s.observed.health = "degraded";
			s.observed.replicasReady = 1;
		}
		expect(healthVerdict(s).detail).toBe("1 of 2 replicas healthy");
	});
	test("stopped, undeployed and missing are not Healthy", () => {
		expect(healthVerdict(service({ desiredState: "stopped" })).label).toBe(
			"Stopped",
		);
		expect(healthVerdict(service({ currentDeploymentId: null })).label).toBe(
			"Not deployed",
		);
		const s = service();
		if (s.observed) s.observed.state = "missing";
		expect(healthVerdict(s).label).toBe("Container missing");
	});
	test("an offline server is called out", () => {
		const s = service();
		if (s.server) s.server.status = "offline";
		expect(healthVerdict(s).label).toBe("Server offline");
	});
});

describe("replicas, restarts, uptime", () => {
	test("counts from the observed state", () => {
		expect(replicaCounts(service())).toEqual({ ready: 2, desired: 2 });
		expect(restartTotal(service())).toBe(3);
	});
	test("falls back to the scaling setting without observation", () => {
		expect(replicaCounts(service({ observed: null }))).toEqual({
			ready: 1,
			desired: 2,
		});
	});
	test("uptime reads from startedAt only while running", () => {
		const now = Date.parse("2026-10-26T10:05:00Z");
		expect(uptimeMs(service(), now)).toBe(300_000);
		expect(uptimeMs(service({ observed: null }), now)).toBeNull();
	});
	test("formatUptime", () => {
		expect(formatUptime(42_000)).toBe("42s");
		expect(formatUptime(4 * 60_000)).toBe("4m");
		expect(formatUptime((3 * 60 + 12) * 60_000)).toBe("3h 12m");
		expect(formatUptime((14 * 24 + 6) * 3_600_000)).toBe("14d 6h");
	});
});

describe("probeDescription", () => {
	test("none says what healthy means", () => {
		const s = service();
		s.config.healthcheck.type = "none";
		expect(probeDescription(s)).toContain("10s");
	});
});

describe("resource figures", () => {
	test("cpu against a limit", () => {
		const s = service();
		s.config.cpus = 1;
		const figure = cpuFigure(s, { cpuPercent: 84, replicaCount: 2 });
		expect(figure.percent).toBe(42);
		expect(figure.detail).toBe("0.84 / 2 vCPU");
	});
	test("cpu without a limit shows cores", () => {
		const figure = cpuFigure(service(), { cpuPercent: 42, replicaCount: 1 });
		expect(figure.percent).toBeNull();
		expect(figure.headline).toBe("0.42 vCPU");
	});
	test("memory prefers the reported limit, else the configured one", () => {
		const mb = (n: number) => `${Math.round(n / 1048576)} MB`;
		const reported = memoryFigure(
			service(),
			{
				memBytes: 312 * 1048576,
				memLimitBytes: 512 * 1048576,
				replicaCount: 1,
			},
			mb,
		);
		expect(reported.headline).toBe("61%");
		expect(reported.detail).toBe("312 MB / 512 MB");
		const s = service();
		s.config.memoryMb = 256;
		const configured = memoryFigure(
			s,
			{ memBytes: 128 * 1048576, memLimitBytes: 0, replicaCount: 2 },
			mb,
		);
		expect(configured.percent).toBe(25);
	});
});

describe("log level and scaling lines", () => {
	test("logLevel", () => {
		expect(logLevel("08:42:03 INFO POST /x")).toBe("info");
		expect(logLevel('level=warn msg="x"')).toBe("warn");
		expect(logLevel("ERROR boom")).toBe("error");
		expect(logLevel("listening on :8080")).toBeNull();
	});
	test("scale event and autoscaling", () => {
		expect(
			scaleEventLine({
				from: 2,
				to: 3,
				reason: "CPU 84% > 70% for 2m",
				actor: "autoscaler",
			}),
		).toBe("Scaled 2 → 3 · CPU 84% > 70% for 2m · autoscaler");
		expect(autoscalingLine(service().scaling)).toBe("Off");
		expect(
			autoscalingLine({
				...service().scaling,
				autoscaling: { enabled: true, min: 1, max: 3, targetCpuPercent: 70 },
			}),
		).toBe("On · 1–3 replicas at 70% CPU");
	});
});

describe("liveUptimeError", () => {
	test("an error from before the service recovered is not shown", () => {
		expect(
			liveUptimeError({
				state: "up",
				lastError: "HTTP 500",
				consecutiveFailures: 0,
			}),
		).toBeNull();
		expect(liveUptimeError({ state: "up", lastError: "HTTP 500" })).toBeNull();
	});

	test("the error shows while the state is down or checks are failing", () => {
		expect(liveUptimeError({ state: "down", lastError: "HTTP 500" })).toBe(
			"HTTP 500",
		);
		expect(
			liveUptimeError({
				state: "up",
				lastError: "HTTP 500",
				consecutiveFailures: 2,
			}),
		).toBe("HTTP 500");
	});

	test("no error text shows nothing", () => {
		expect(liveUptimeError({ state: "down", lastError: "" })).toBeNull();
	});
});
