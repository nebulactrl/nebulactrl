import { describe, expect, test } from "bun:test";
import type { Service } from "#/api/models";
import { makeDeployment, makeObserved, makeService } from "../lib/fixtures";
import { repoName, servicePill, sourceLine } from "./service-pill";

const base = {
	sourceType: "image",
	config: { image: "postgres:17" },
	domains: [],
	desiredState: "running",
	currentDeploymentId: "d1",
	latestDeployment: { status: "succeeded", finishedAt: null },
	server: { status: "online", name: "agent1", lastSeenAt: null },
	observed: { state: "running", health: "healthy" },
} as unknown as Service;

function with_(patch: Record<string, unknown>): Service {
	return { ...base, ...patch } as unknown as Service;
}

describe("repoName", () => {
	test("reads owner/repo from https and scp-style URLs", () => {
		expect(repoName("https://github.com/acme/shop-api.git")).toBe(
			"acme/shop-api",
		);
		expect(repoName("git@github.com:acme/shop-api.git")).toBe("acme/shop-api");
		expect(repoName("https://git.example.com")).toBe("git.example.com");
	});
});

describe("sourceLine", () => {
	test("an image with domains", () => {
		const line = sourceLine(
			with_({ domains: [{ hostname: "a.dev" }, { hostname: "b.dev" }] }),
		);
		expect(line.kind).toBe("image");
		expect(line.parts).toEqual(["postgres:17"]);
		expect(line.domain).toEqual({ hostname: "a.dev", more: 1 });
	});
	test("a git repo with branch", () => {
		const line = sourceLine(
			with_({
				sourceType: "git",
				config: {
					gitUrl: "https://github.com/acme/shop-api",
					gitBranch: "main",
				},
				domains: [{ hostname: "api.acme.dev" }],
			}),
		);
		expect(line.parts).toEqual(["acme/shop-api", "main"]);
		expect(line.domain).toEqual({ hostname: "api.acme.dev", more: 0 });
	});
	test("no domain, no link", () => {
		expect(sourceLine(base).domain).toBeNull();
	});
});

describe("servicePill", () => {
	test("a running healthy service is Live", () => {
		expect(servicePill(base).label).toBe("Live");
	});
	test("stopped, offline and degraded outrank Live", () => {
		expect(servicePill(with_({ desiredState: "stopped" })).label).toBe(
			"Stopped",
		);
		expect(
			servicePill(with_({ server: { status: "offline", name: "x" } })).label,
		).toBe("Offline");
		expect(
			servicePill(with_({ observed: { state: "running", health: "degraded" } }))
				.label,
		).toBe("Degraded");
	});
	test("an in-flight build spins", () => {
		const pill = servicePill(
			with_({ latestDeployment: { status: "building", finishedAt: null } }),
		);
		expect(pill).toEqual({ kind: "building", label: "Building" });
	});
});

describe("servicePill by deployment state", () => {
	test("a deployment waiting on an offline server is Offline, not Deploying", () => {
		const pill = servicePill(
			makeService({
				latestDeployment: makeDeployment("queued"),
				server: { id: "s", name: "edge", status: "offline", lastSeenAt: null },
			}),
		);
		expect(pill).toEqual({ kind: "offline", label: "Offline" });
	});

	test("a queued deployment is Deploying, a building one is Building", () => {
		expect(
			servicePill(makeService({ latestDeployment: makeDeployment("queued") })),
		).toEqual({ kind: "building", label: "Building" });
		expect(
			servicePill(
				makeService({ latestDeployment: makeDeployment("releasing") }),
			),
		).toEqual({ kind: "deploying", label: "Deploying" });
	});

	test("a failed deployment is Failed whatever serves", () => {
		expect(
			servicePill(
				makeService({
					latestDeployment: makeDeployment("failed"),
					currentDeploymentId: "dep-old",
				}),
			),
		).toEqual({ kind: "failed", label: "Failed" });
	});

	test("a rolled back deployment is Rolled back, in the degraded colour", () => {
		expect(
			servicePill(
				makeService({ latestDeployment: makeDeployment("rolled_back") }),
			),
		).toEqual({ kind: "degraded", label: "Rolled back" });
	});

	test("a service never deployed is Not deployed", () => {
		expect(servicePill(makeService())).toEqual({
			kind: "unknown",
			label: "Not deployed",
		});
	});

	test("a revoked server makes a deployed service Offline", () => {
		expect(
			servicePill(
				makeService({
					currentDeploymentId: "d1",
					latestDeployment: makeDeployment("succeeded"),
					observed: makeObserved(),
					server: {
						id: "s",
						name: "edge",
						status: "revoked",
						lastSeenAt: null,
					},
				}),
			).label,
		).toBe("Offline");
	});

	test("maintenance mode outranks health but not a stopped service", () => {
		const deployed = {
			currentDeploymentId: "d1",
			latestDeployment: makeDeployment("succeeded"),
			observed: makeObserved({ health: "unhealthy" }),
			maintenance: {
				enabled: true,
				title: "",
				message: "",
				eta: null,
				since: null,
			},
		};
		expect(servicePill(makeService(deployed))).toEqual({
			kind: "warning",
			label: "Maintenance",
		});
		expect(
			servicePill(makeService({ ...deployed, desiredState: "stopped" })).label,
		).toBe("Stopped");
	});
});

describe("repoName edge cases", () => {
	test("a URL with only a host is the host", () => {
		expect(repoName("https://git.example.com/")).toBe("git.example.com");
	});

	test("trims whitespace and keeps nested group paths", () => {
		expect(repoName("  https://gitlab.com/group/sub/app.git  ")).toBe(
			"group/sub/app",
		);
	});

	test("text that is not a URL loses only a scheme-like prefix", () => {
		expect(repoName("ssh://not a url")).toBe("not a url");
		expect(repoName("plain/path")).toBe("plain/path");
	});
});

describe("sourceLine kinds", () => {
	test("a route names its upstream and has no domain link", () => {
		const line = sourceLine(
			makeService({
				kind: "route",
				sourceType: "external",
				config: {
					...makeService().config,
					route: {
						protocol: "http",
						upstreams: [{ address: "192.168.1.20:8123" }],
					} as never,
				},
			}),
		);
		expect(line.kind).toBe("route");
		expect(line.parts).toEqual(["192.168.1.20:8123"]);
	});

	test("a route without config falls back to its private host", () => {
		const line = sourceLine(
			makeService({
				kind: "route",
				sourceType: "external",
				privateHost: "plex-route",
			}),
		);
		expect(line.parts).toEqual(["plex-route"]);
	});

	test("a stack is Docker Compose with its component count", () => {
		const line = sourceLine(
			makeService({
				kind: "stack",
				sourceType: "git",
				config: {
					...makeService().config,
					stack: { components: [{ name: "web" }, { name: "db" }] } as never,
				},
			}),
		);
		expect(line.kind).toBe("compose");
		expect(line.parts).toEqual(["Docker Compose", "2 components"]);
	});

	test("a stack with one component says component in the singular", () => {
		const line = sourceLine(
			makeService({
				kind: "stack",
				config: {
					...makeService().config,
					stack: { components: [{ name: "web" }] } as never,
				},
			}),
		);
		expect(line.parts[1]).toBe("1 component");
	});

	test("a git service without a branch shows only the repo", () => {
		const line = sourceLine(
			makeService({
				sourceType: "git",
				config: {
					...makeService().config,
					image: "",
					gitUrl: "https://github.com/acme/shop.git",
					gitBranch: "",
				},
			}),
		);
		expect(line.kind).toBe("git");
		expect(line.parts).toEqual(["acme/shop"]);
	});

	test("a service with no source set has nothing to say", () => {
		const line = sourceLine(
			makeService({ config: { ...makeService().config, image: "" } }),
		);
		expect(line).toEqual({ kind: "empty", parts: [], domain: null });
	});
});

describe("servicePill: a cron service", () => {
	const cron = with_({
		kind: "worker",
		observed: null,
		config: {
			image: "busybox",
			oneshot: true,
			cron: { schedule: "* * * * *" },
		},
		cron: { taskId: "t", schedule: "* * * * *", timezone: "UTC" },
	});

	test("between runs it is Scheduled, not Live", () => {
		expect(servicePill(cron)).toEqual({ kind: "live", label: "Scheduled" });
	});

	test("a run in progress is Running; a failed last run says so", () => {
		const run = (status: string) =>
			with_({
				...cron,
				cron: {
					taskId: "t",
					schedule: "* * * * *",
					timezone: "UTC",
					lastRun: { id: "r", status },
				},
			});
		expect(servicePill(run("running"))).toEqual({
			kind: "deploying",
			label: "Running",
		});
		expect(servicePill(run("timed_out"))).toEqual({
			kind: "failed",
			label: "Last run failed",
		});
		expect(servicePill(run("succeeded")).label).toBe("Scheduled");
	});

	test("stopped is Paused, and an offline server outranks Scheduled", () => {
		expect(servicePill(with_({ ...cron, desiredState: "stopped" }))).toEqual({
			kind: "stopped",
			label: "Paused",
		});
		expect(
			servicePill(with_({ ...cron, server: { status: "offline", name: "x" } }))
				.label,
		).toBe("Offline");
	});

	test("a build in flight keeps its own pill", () => {
		expect(
			servicePill(
				with_({
					...cron,
					latestDeployment: { status: "building", finishedAt: null },
				}),
			).label,
		).toBe("Building");
	});
});
