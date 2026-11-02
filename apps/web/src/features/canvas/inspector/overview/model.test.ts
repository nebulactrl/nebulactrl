import { describe, expect, test } from "bun:test";
import type { Service } from "#/api/models";
import {
	autoscalingLine,
	configuredLimits,
	cpuFigure,
	formatUptime,
	healthVerdict,
	liveUptimeError,
	logLevel,
	memoryFigure,
	probeDescription,
	replicaCounts,
	replicaKind,
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
	test("a service with processes falls back to the running processes' sums", () => {
		const process = (
			name: string,
			replicas: number,
			ready: number,
			pending = false,
		) => ({ name, pending, scaling: { replicas, ready } });
		const s = service({
			observed: null,
			processes: [
				process("web", 1, 1),
				process("worker", 2, 1),
				process("cron", 1, 0, true),
			],
		} as unknown as Partial<Service>);
		expect(replicaCounts(s)).toEqual({ ready: 2, desired: 3 });
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
	test("cpu of several processes assumes no limit from the config", () => {
		const base = service();
		const s = service({
			processes: [{ name: "web" }, { name: "worker" }],
			config: {
				...base.config,
				cpus: 1,
				memoryMb: 256,
				processes: [{ name: "worker", cpus: 2 }],
			},
		} as unknown as Partial<Service>);
		const cpu = cpuFigure(s, { cpuPercent: 84, replicaCount: 2 });
		expect(cpu.percent).toBeNull();
		expect(cpu.headline).toBe("0.84 vCPU");
		const memory = memoryFigure(
			s,
			{ memBytes: 128 * 1048576, memLimitBytes: 0, replicaCount: 2 },
			(n) => `${Math.round(n / 1048576)} MB`,
		);
		expect(memory.percent).toBeNull();
		expect(memory.detail).toBe("No memory limit set");
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

describe("probeDescription by check type", () => {
	function withCheck(check: Record<string, unknown>, port = 8080): Service {
		const s = service();
		s.config.port = port;
		s.config.healthcheck = {
			...s.config.healthcheck,
			...check,
		} as Service["config"]["healthcheck"];
		return s;
	}

	test("http names the path and the port, defaulting the path to /", () => {
		expect(probeDescription(withCheck({ path: "" }))).toBe(
			"HTTP GET / on :8080 · every 2s",
		);
	});

	test("the check's own port wins over the service port", () => {
		expect(probeDescription(withCheck({ port: 9000 }))).toBe(
			"HTTP GET /healthz on :9000 · every 2s",
		);
	});

	test("no port anywhere leaves it out", () => {
		expect(probeDescription(withCheck({}, 0))).toBe(
			"HTTP GET /healthz · every 2s",
		);
	});

	test("tcp names the connect", () => {
		expect(probeDescription(withCheck({ type: "tcp" }))).toBe(
			"TCP connect :8080 · every 2s",
		);
		expect(probeDescription(withCheck({ type: "tcp" }, 0))).toBe(
			"TCP connect · every 2s",
		);
	});

	test("docker defers to the image", () => {
		expect(probeDescription(withCheck({ type: "docker" }))).toBe(
			"The image's own HEALTHCHECK",
		);
	});
});

describe("replicaKind", () => {
	test("a replica that is not running is failed", () => {
		expect(replicaKind({ state: "exited", health: "healthy" })).toBe("failed");
	});
	test("a running unhealthy replica is failed", () => {
		expect(replicaKind({ state: "running", health: "unhealthy" })).toBe(
			"failed",
		);
	});
	test("running and healthy is live, anything else is starting", () => {
		expect(replicaKind({ state: "running", health: "healthy" })).toBe("live");
		expect(replicaKind({ state: "running", health: "starting" })).toBe(
			"building",
		);
		expect(replicaKind({ state: "running", health: "" })).toBe("building");
	});
});

describe("healthVerdict states", () => {
	test("a first release in flight is Deploying", () => {
		const verdict = healthVerdict(
			service({
				currentDeploymentId: null,
				latestDeployment: { status: "building" } as Service["latestDeployment"],
			}),
		);
		expect(verdict).toEqual({
			kind: "deploying",
			label: "Deploying",
			detail: "The first release is on its way",
		});
	});

	test("a first release that already failed is Not deployed", () => {
		expect(
			healthVerdict(
				service({
					currentDeploymentId: null,
					latestDeployment: {
						status: "failed",
					} as Service["latestDeployment"],
				}),
			).label,
		).toBe("Not deployed");
	});

	test("an offline server names itself", () => {
		const s = service();
		if (s.server) s.server.status = "revoked";
		expect(healthVerdict(s)).toEqual({
			kind: "offline",
			label: "Server offline",
			detail: "agent1 is not connected; the last report may be stale",
		});
	});

	test("no observation yet is Not reported, never a made-up probe result", () => {
		expect(healthVerdict(service({ observed: null }))).toEqual({
			kind: "offline",
			label: "Not reported",
			detail: "The agent has not reported this service yet",
		});
	});

	test("a container that is not running says its state, or the agent's error", () => {
		const exited = service();
		if (exited.observed) exited.observed.state = "exited";
		expect(healthVerdict(exited)).toEqual({
			kind: "failed",
			label: "Not running",
			detail: "Container state: exited",
		});
		const errored = service();
		if (errored.observed) {
			errored.observed.state = "exited";
			errored.observed.error = "OOM killed";
		}
		expect(healthVerdict(errored).detail).toBe("OOM killed");
	});

	test("a running container whose probe fails is Unhealthy", () => {
		const s = service();
		if (s.observed) s.observed.health = "unhealthy";
		expect(healthVerdict(s)).toEqual({
			kind: "failed",
			label: "Unhealthy",
			detail: "HTTP GET /healthz on :8080 · every 2s",
		});
	});

	test("without a health check a running container is Running, with one it is Starting", () => {
		const unchecked = service();
		unchecked.config.healthcheck.type = "none";
		if (unchecked.observed) unchecked.observed.health = "none";
		expect(healthVerdict(unchecked).label).toBe("Running");
		expect(healthVerdict(unchecked).kind).toBe("live");
		const starting = service();
		if (starting.observed) starting.observed.health = "starting";
		expect(healthVerdict(starting).label).toBe("Starting");
		expect(healthVerdict(starting).kind).toBe("building");
	});
});

describe("uptimeMs and limits edge cases", () => {
	test("a start time that does not parse is no uptime", () => {
		const s = service();
		if (s.observed) s.observed.startedAt = "yesterday-ish";
		expect(uptimeMs(s, Date.now())).toBeNull();
	});

	test("a missing start time is no uptime", () => {
		const s = service();
		if (s.observed) s.observed.startedAt = null;
		expect(uptimeMs(s, Date.now())).toBeNull();
	});

	test("a clock behind the start never goes negative", () => {
		expect(uptimeMs(service(), Date.parse("2026-10-26T09:00:00Z"))).toBe(0);
	});

	test("a container that is not running has no uptime", () => {
		const s = service();
		if (s.observed) s.observed.state = "exited";
		expect(uptimeMs(s, Date.parse("2026-10-26T10:05:00Z"))).toBeNull();
	});

	test("configured limits come straight from the config for a plain service", () => {
		const s = service();
		s.config.cpus = 2;
		s.config.memoryMb = 512;
		expect(configuredLimits(s)).toEqual({ cpus: 2, memoryMb: 512 });
	});

	test("formatUptime of a negative span is 0s", () => {
		expect(formatUptime(-5000)).toBe("0s");
	});
});

describe("logLevel details", () => {
	test("WARNING and FATAL/PANIC/ERR map to warn and error", () => {
		expect(logLevel("WARNING disk almost full")).toBe("warn");
		expect(logLevel("FATAL out of memory")).toBe("error");
		expect(logLevel("panic: nil map")).toBe("error");
		expect(logLevel("ERR bad request")).toBe("error");
	});

	test("a level word past the first 64 characters is just text", () => {
		expect(logLevel(`${"x".repeat(70)} ERROR`)).toBeNull();
	});

	test("a word that merely contains a level is not a level", () => {
		expect(logLevel("information request failed")).toBeNull();
	});
});

describe("scale event line without a reason", () => {
	test("leaves the reason out", () => {
		expect(scaleEventLine({ from: 1, to: 2, reason: "", actor: "ada" })).toBe(
			"Scaled 1 → 2 · ada",
		);
	});
});

describe("resource figures at the edges", () => {
	test("no sample says so", () => {
		expect(cpuFigure(service(), undefined)).toEqual({
			percent: null,
			headline: "—",
			detail: "No samples yet",
		});
		expect(memoryFigure(service(), undefined, String)).toEqual({
			percent: null,
			headline: "—",
			detail: "No samples yet",
		});
	});

	test("cpu over the limit caps at 100%", () => {
		const s = service();
		s.config.cpus = 1;
		expect(cpuFigure(s, { cpuPercent: 300, replicaCount: 1 }).percent).toBe(
			100,
		);
	});

	test("memory with no limit anywhere shows the bytes in use", () => {
		const figure = memoryFigure(
			service(),
			{ memBytes: 2048, memLimitBytes: 0, replicaCount: 1 },
			(n) => `${n} B`,
		);
		expect(figure).toEqual({
			percent: null,
			headline: "2048 B",
			detail: "No memory limit set",
		});
	});
});
