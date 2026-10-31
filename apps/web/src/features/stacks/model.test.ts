import { describe, expect, test } from "bun:test";
import {
	alignResources,
	buildResources,
	type ComponentReplica,
	checkVerdict,
	componentLabel,
	componentName,
	componentRows,
	componentState,
	defaultDomainPort,
	domainBlocker,
	domainTarget,
	groupViolations,
	nodeSummary,
	replicaLabel,
	resourceErrors,
	type StackComponent,
	shortDigest,
	stackComponents,
	stackNodeSize,
	toResourcesDraft,
	verdictText,
	waitSecondsError,
} from "./model";

const COMPONENTS = [
	{ name: "cache", index: 0, image: "redis:7", ports: [6379] },
	{ name: "db", index: 1, image: "postgres:16", ports: [5432] },
	{ name: "web", index: 2, image: "nginx:1.27", ports: [80, 8080] },
];

function replica(
	index: number,
	component: string | undefined,
	state = "running",
	health = "healthy",
	restartCount = 0,
) {
	return { index, component, state, health, restartCount, startedAt: null };
}

describe("component mapping", () => {
	test("maps indexes and names through config.stack.components", () => {
		expect(componentName(COMPONENTS, 1)).toBe("db");
		expect(componentName(COMPONENTS, 9)).toBeUndefined();
		expect(componentLabel(COMPONENTS, 2)).toBe("web");
		expect(componentLabel(COMPONENTS, 7)).toBe("#7");
	});

	test("labels replicas by component for a stack and by number otherwise", () => {
		expect(replicaLabel(COMPONENTS, 0)).toBe("cache");
		expect(replicaLabel([], 2)).toBe("Replica 2");
	});

	test("only a stack has components", () => {
		const config = { stack: { components: COMPONENTS } };
		expect(stackComponents({ kind: "stack", config })).toHaveLength(3);
		expect(stackComponents({ kind: "app", config })).toHaveLength(0);
		expect(stackComponents({ kind: "stack", config: {} })).toHaveLength(0);
	});
});

describe("component states", () => {
	test("reads state and health in words", () => {
		expect(componentState({ state: "running", health: "healthy" })).toEqual({
			kind: "healthy",
			label: "Healthy",
		});
		expect(componentState({ state: "running", health: "unhealthy" }).kind).toBe(
			"failed",
		);
		expect(componentState({ state: "running", health: "starting" })).toEqual({
			kind: "building",
			label: "Starting",
		});
		expect(componentState({ state: "running", health: "unknown" })).toEqual({
			kind: "live",
			label: "Running",
		});
		expect(
			componentState({ state: "restarting", health: "unknown" }).label,
		).toBe("Restarting");
	});

	test("a component that exited 0 is completed, any other exit is a failure", () => {
		expect(componentState({ state: "exited", health: "healthy" })).toEqual({
			kind: "healthy",
			label: "Completed",
		});
		expect(componentState({ state: "exited", health: "unhealthy" })).toEqual({
			kind: "failed",
			label: "Exited",
		});
	});

	test("a component without a replica is not started", () => {
		expect(componentState(undefined)).toEqual({
			kind: "pending",
			label: "Not started",
		});
	});
});

describe("componentRows", () => {
	test("joins replicas by name, usage by index and digests by name", () => {
		const rows = componentRows({
			components: COMPONENTS,
			replicas: [
				replica(2, "web", "running", "healthy", 3),
				replica(1, "db", "running", "starting"),
			],
			usage: [
				{ index: 2, cpuPercent: 12.5, memBytes: 1000 },
				{ index: 1, cpuPercent: 1, memBytes: 2000 },
				{ index: 0, cpuPercent: 99, memBytes: 3000 },
			],
			digests: { web: "docker.io/library/nginx@sha256:abcdef0123456789aaaa" },
		});
		expect(rows.map((r) => r.name)).toEqual(["cache", "db", "web"]);
		const [cache, db, web] = rows;
		expect(web).toMatchObject({
			restarts: 3,
			cpuPercent: 12.5,
			memBytes: 1000,
			digest: "sha256:abcdef012345",
			running: true,
		});
		expect(db?.state.label).toBe("Starting");
		// No replica reported: no usage is shown even if a sample has its index.
		expect(cache).toMatchObject({
			cpuPercent: null,
			memBytes: null,
			digest: null,
			running: false,
		});
		expect(cache?.state.label).toBe("Not started");
	});

	test("falls back to the index when the agent sends no component name", () => {
		const rows = componentRows({
			components: COMPONENTS,
			replicas: [replica(1, undefined)],
			usage: [],
			digests: undefined,
		});
		expect(rows.find((r) => r.name === "db")?.state.label).toBe("Healthy");
	});

	test("a replica of a component the config dropped still gets a row", () => {
		const rows = componentRows({
			components: [COMPONENTS[0] as (typeof COMPONENTS)[number]],
			replicas: [replica(0, "cache"), replica(5, "old")],
			usage: [],
			digests: null,
		});
		expect(rows.map((r) => r.name)).toEqual(["cache", "old"]);
	});

	test("shortDigest keeps the algorithm and twelve hex digits", () => {
		expect(shortDigest("repo@sha256:0123456789abcdef")).toBe(
			"sha256:0123456789ab",
		);
		expect(shortDigest("sha256:ffffffff")).toBe("sha256:ffffffff");
		expect(shortDigest("nginx:1.27")).toBeNull();
		expect(shortDigest(undefined)).toBeNull();
	});
});

describe("violations", () => {
	const violations = [
		{ service: "web", key: "privileged", message: "no", kind: "restricted" },
		{ service: "db", key: "build", message: "no build", kind: "unsupported" },
		{
			service: "web",
			key: "network_mode",
			message: "host",
			kind: "restricted",
		},
		{ key: "include", message: "include", kind: "unsupported" },
		{ service: "web", key: "devices", message: "dev", kind: "unsupported" },
	];

	test("groups by service, the file first, unsupported apart from restricted", () => {
		const groups = groupViolations(violations);
		expect(groups.map((g) => g.service)).toEqual(["", "db", "web"]);
		const web = groups[2];
		expect(web?.restricted.map((v) => v.key)).toEqual([
			"privileged",
			"network_mode",
		]);
		expect(web?.unsupported.map((v) => v.key)).toEqual(["devices"]);
		expect(groupViolations(null)).toEqual([]);
	});

	test("an unrestricted server lifts restricted problems only", () => {
		expect(checkVerdict({ violations, unrestricted: false })).toEqual({
			blocked: true,
			unsupported: 3,
			restricted: 2,
			needsAdmin: true,
		});
		expect(checkVerdict({ violations, unrestricted: true })).toEqual({
			blocked: true,
			unsupported: 3,
			restricted: 0,
			needsAdmin: false,
		});
		const onlyRestricted = violations.filter((v) => v.kind === "restricted");
		expect(
			checkVerdict({ violations: onlyRestricted, unrestricted: true }).blocked,
		).toBe(false);
		expect(checkVerdict({ violations: [], unrestricted: false }).blocked).toBe(
			false,
		);
	});

	test("an unknown kind is treated as unsupported", () => {
		expect(
			checkVerdict({
				violations: [{ key: "x", message: "m", kind: "other" }],
				unrestricted: true,
			}).unsupported,
		).toBe(1);
	});

	test("the verdict text names the counts", () => {
		expect(
			verdictText(checkVerdict({ violations: [], unrestricted: false })),
		).toBe("The Compose file passes the checks.");
		expect(verdictText(checkVerdict({ violations, unrestricted: false }))).toBe(
			"5 problems: 3 not supported, 2 restricted on this server.",
		);
	});
});

describe("stack node summary", () => {
	const source = (
		replicas: ComponentReplica[],
		components: StackComponent[] = COMPONENTS,
		domains: { hostname: string; component?: string }[] = [],
	) => ({
		kind: "stack",
		config: { stack: { components } },
		observed: { replicas },
		domains,
	});

	test("lists every component in index order with route and restarts", () => {
		const summary = nodeSummary(
			source(
				[
					replica(0, "cache"),
					replica(1, "db"),
					replica(2, "web", "running", "healthy", 2),
				],
				COMPONENTS,
				[{ hostname: "app.example.com", component: "web" }],
			),
		);
		expect(summary.more).toBe(0);
		expect(summary.total).toBe(3);
		expect(summary.shown.map((c) => [c.name, c.route, c.restarts])).toEqual([
			["cache", ":6379", 0],
			["db", ":5432", 0],
			["web", "app.example.com", 2],
		]);
	});

	test("a component without a domain or port has no route", () => {
		const summary = nodeSummary(source([], [{ name: "worker", index: 0 }]));
		expect(summary.shown[0]?.route).toBeNull();
		expect(summary.shown[0]?.state.label).toBe("Not started");
	});

	test("clamps at eight rows with +N and keeps problems out of the overflow", () => {
		const many = Array.from({ length: 11 }, (_, index) => ({
			name: `c${String(index).padStart(2, "0")}`,
			index,
		}));
		const replicas = many.map((component) =>
			replica(
				component.index,
				component.name,
				"running",
				component.index === 10 ? "unhealthy" : "healthy",
			),
		);
		const summary = nodeSummary(source(replicas, many));
		expect(summary.shown).toHaveLength(8);
		expect(summary.more).toBe(3);
		expect(summary.total).toBe(11);
		expect(summary.shown[0]?.name).toBe("c10");
	});

	test("keeps index order while everything fits", () => {
		const summary = nodeSummary(
			source([replica(0, "cache", "running", "unhealthy"), replica(1, "db")]),
		);
		expect(summary.shown.map((c) => c.name)).toEqual(["cache", "db", "web"]);
	});

	test("the card height follows the rows and the +N line", () => {
		const base = stackNodeSize({ shown: [], more: 0 });
		const three = stackNodeSize({ shown: new Array(3).fill(0), more: 0 });
		const full = stackNodeSize({ shown: new Array(8).fill(0), more: 3 });
		expect(three.h).toBeGreaterThan(base.h - 1);
		expect(three.h - base.h).toBe(2 * 28);
		expect(full.h - three.h).toBe(5 * 28 + 28);
		expect(three.w).toBe(full.w);
	});
});

describe("component limits", () => {
	test("round-trips and drops components with no limit", () => {
		const draft = toResourcesDraft({
			web: { memoryMb: 256, cpus: 0.5 },
			db: {},
		});
		expect(draft).toEqual({
			web: { memoryMb: "256", cpus: "0.5" },
			db: { memoryMb: "", cpus: "" },
		});
		expect(buildResources(draft)).toEqual({
			web: { memoryMb: 256, cpus: 0.5 },
		});
		expect(buildResources({ web: { memoryMb: "", cpus: "1.5" } })).toEqual({
			web: { cpus: 1.5 },
		});
	});

	test("validates memory and CPU bounds per component", () => {
		expect(
			resourceErrors({
				web: { memoryMb: "5", cpus: "0" },
				db: { memoryMb: "512", cpus: "2" },
				cache: { memoryMb: "1.5", cpus: "abc" },
			}),
		).toEqual({
			web: {
				memoryMb: "Memory must be a whole number from 6 to 1048576 MB.",
				cpus: "CPUs must be a number between 0.01 and 256.",
			},
			cache: {
				memoryMb: "Memory must be a whole number from 6 to 1048576 MB.",
				cpus: "CPUs must be a number between 0.01 and 256.",
			},
		});
	});

	test("aligns drafts with the components the file declares", () => {
		const aligned = alignResources(
			{ web: { memoryMb: "256", cpus: "" }, gone: { memoryMb: "1", cpus: "" } },
			[{ name: "web" }, { name: "db" }],
		);
		expect(Object.keys(aligned)).toEqual(["web", "db"]);
		expect(aligned.web?.memoryMb).toBe("256");
		expect(aligned.db).toEqual({ memoryMb: "", cpus: "" });
	});

	test("wait timeout is empty or 1 to 3600 seconds", () => {
		expect(waitSecondsError("")).toBeUndefined();
		expect(waitSecondsError("300")).toBeUndefined();
		expect(waitSecondsError("0")).toBeDefined();
		expect(waitSecondsError("3601")).toBeDefined();
		expect(waitSecondsError("1.5")).toBeDefined();
	});
});

describe("domains", () => {
	test("a single declared port is the default", () => {
		expect(defaultDomainPort(COMPONENTS[1])).toBe(5432);
		expect(defaultDomainPort(COMPONENTS[2])).toBeUndefined();
		expect(defaultDomainPort({})).toBeUndefined();
		expect(defaultDomainPort(undefined)).toBeUndefined();
	});

	test("the add-domain form says what is missing", () => {
		const input = { hostname: "a.example.com", components: COMPONENTS };
		expect(domainBlocker({ ...input, component: "", port: "" })).toBe(
			"Pick the component the domain serves.",
		);
		expect(domainBlocker({ ...input, component: "db", port: "" })).toBeNull();
		expect(domainBlocker({ ...input, component: "web", port: "" })).toBe(
			"web declares several ports: enter the port.",
		);
		expect(
			domainBlocker({
				...input,
				components: [{ name: "job", index: 0 }],
				component: "job",
				port: "",
			}),
		).toBe("job declares no port: enter the port.");
		expect(domainBlocker({ ...input, component: "web", port: "70000" })).toBe(
			"The port must be a number from 1 to 65535.",
		);
		expect(
			domainBlocker({ ...input, component: "web", port: "8080" }),
		).toBeNull();
		expect(
			domainBlocker({ ...input, components: [], component: "x", port: "1" }),
		).toBe("The stack has no components yet.");
	});

	test("names the target of a domain", () => {
		expect(domainTarget({ component: "web", targetPort: 80 })).toBe("web:80");
		expect(domainTarget({ component: "web", targetPort: 0 })).toBe("web");
		expect(domainTarget({ component: "", targetPort: 80 })).toBeNull();
	});
});
