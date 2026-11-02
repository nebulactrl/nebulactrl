import { describe, expect, test } from "bun:test";
import type * as Gen from "./gen/types.gen";
import {
	asDeploymentStatus,
	fromCanvasLayout,
	PLAIN_DOMAIN_RULE,
	productionFirst,
	toCanvasDocument,
	toDeployment,
	toPlan,
	toServer,
	toService,
	toVariable,
} from "./models";

/** A wire object with only the fields a test names; the rest is whatever the converter must tolerate. */
function wire<T>(fields: Record<string, unknown>): T {
	return fields as unknown as T;
}

const scaling = (extra: Record<string, unknown> = {}) =>
	wire<Gen.ScalingView>({
		replicas: 1,
		lastScaleEvent: null,
		blockedReason: "",
		ineligibleReason: "",
		...extra,
	});

function serviceView(extra: Record<string, unknown> = {}): Gen.ServiceView {
	return wire<Gen.ServiceView>({
		id: "svc-1",
		environmentId: "env-1",
		projectId: "proj-1",
		name: "Web",
		slug: "web",
		kind: "app",
		sourceType: "image",
		config: {
			startCommand: null,
			healthcheck: { type: "http", path: "/up" },
			processes: null,
		},
		serverId: "",
		server: null,
		generation: 4,
		appliedGeneration: 3,
		hasStagedChanges: true,
		currentDeploymentId: "",
		latestDeployment: null,
		currentRelease: null,
		observed: null,
		observedAt: null,
		stale: false,
		domains: null,
		privateHost: "web.internal",
		gitTokenSet: false,
		registryAuthSet: false,
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-02T00:00:00Z",
		desiredState: "running",
		volumes: null,
		scaling: scaling(),
		pendingChangeCounts: { total: 0 },
		stagedKind: "",
		maintenance: { enabled: false },
		git: null,
		tailnet: null,
		networks: null,
		processes: null,
		...extra,
	});
}

describe("asDeploymentStatus", () => {
	test("keeps a known status", () => {
		expect(asDeploymentStatus("failed")).toBe("failed");
		expect(asDeploymentStatus("canceled")).toBe("canceled");
	});

	test("reads a status this build does not know as failed rather than rendering it blind", () => {
		expect(asDeploymentStatus("exploded")).toBe("failed");
		expect(asDeploymentStatus("")).toBe("failed");
	});
});

describe("toService", () => {
	test("turns every null collection and empty id of a bare service into an empty value", () => {
		const service = toService(serviceView());
		expect(service.config.startCommand).toEqual([]);
		expect(service.config.processes).toEqual([]);
		expect(service.config.healthcheck.type).toBe("http");
		expect(service.domains).toEqual([]);
		expect(service.volumes).toEqual([]);
		expect(service.networks).toEqual([]);
		expect(service.processes).toEqual([]);
		expect(service.serverId).toBeNull();
		expect(service.server).toBeNull();
		expect(service.currentDeploymentId).toBeNull();
		expect(service.latestDeployment).toBeNull();
		expect(service.currentRelease).toBeNull();
		expect(service.observed).toBeNull();
		expect(service.observedAt).toBeNull();
		expect(service.git).toBeNull();
		expect(service.tailnet).toBeNull();
		expect(service.stagedKind).toBeNull();
		expect(service.scaling).toEqual({
			replicas: 1,
			lastScaleEvent: null,
			blockedReason: null,
			ineligibleReason: null,
		} as never);
	});

	test("passes scalar fields through untouched", () => {
		const service = toService(serviceView());
		expect(service).toMatchObject({
			id: "svc-1",
			environmentId: "env-1",
			projectId: "proj-1",
			name: "Web",
			slug: "web",
			kind: "app",
			sourceType: "image",
			generation: 4,
			appliedGeneration: 3,
			hasStagedChanges: true,
			privateHost: "web.internal",
			desiredState: "running",
			stale: false,
		});
	});

	test("keeps a server only when it has an id, and null last-seen stays null", () => {
		expect(
			toService(serviceView({ server: { id: "", name: "ghost" } })).server,
		).toBeNull();
		const service = toService(
			serviceView({
				serverId: "srv-1",
				server: { id: "srv-1", name: "edge-1", status: "online" },
			}),
		);
		expect(service.serverId).toBe("srv-1");
		expect(service.server).toEqual({
			id: "srv-1",
			name: "edge-1",
			status: "online",
			lastSeenAt: null,
		});
	});

	test("maps the latest deployment, normalising its status and blank strings", () => {
		const service = toService(
			serviceView({
				latestDeployment: {
					id: "dep-1",
					status: "mystery",
					trigger: "manual",
					strategy: "recreate",
					statusMessage: "",
					createdAt: "2026-01-03T00:00:00Z",
					releaseId: "",
					message: "ship it",
					failedStep: "",
					currentStep: "build",
				},
			}),
		);
		expect(service.latestDeployment).toEqual({
			id: "dep-1",
			status: "failed",
			trigger: "manual",
			strategy: "recreate",
			statusMessage: null,
			createdAt: "2026-01-03T00:00:00Z",
			finishedAt: null,
			releaseId: null,
			message: "ship it",
			failedStep: null,
			currentStep: "build",
		});
	});

	test("drops a latest deployment, release, git link and tailnet that carry no identity", () => {
		const service = toService(
			serviceView({
				latestDeployment: { id: "" },
				currentRelease: { id: "" },
				git: { connectionId: "" },
				tailnet: { state: "" },
			}),
		);
		expect(service.latestDeployment).toBeNull();
		expect(service.currentRelease).toBeNull();
		expect(service.git).toBeNull();
		expect(service.tailnet).toBeNull();
	});

	test("keeps a release, git link and tailnet that do", () => {
		const release = { id: "rel-1", imageRef: "nginx:1", source: {} };
		const git = { connectionId: "c-1", repo: "a/b" };
		const tailnet = { state: "up", hostname: "web" };
		const service = toService(
			serviceView({ currentRelease: release, git, tailnet }),
		);
		expect(service.currentRelease).toEqual(release as never);
		expect(service.git).toEqual(git as never);
		expect(service.tailnet).toEqual(tailnet as never);
	});

	test("reports observed state only once the agent reported a container and a state", () => {
		expect(
			toService(serviceView({ observed: { containerId: "c1", state: "" } }))
				.observed,
		).toBeNull();
		expect(
			toService(serviceView({ observed: { state: "running" } })).observed,
		).toBeNull();
		const observed = { containerId: "c1", state: "running" };
		expect(toService(serviceView({ observed })).observed).toEqual(
			observed as never,
		);
	});

	test("only new and edited are staged kinds", () => {
		expect(toService(serviceView({ stagedKind: "new" })).stagedKind).toBe(
			"new",
		);
		expect(toService(serviceView({ stagedKind: "edited" })).stagedKind).toBe(
			"edited",
		);
		expect(
			toService(serviceView({ stagedKind: "bogus" })).stagedKind,
		).toBeNull();
	});

	test("normalises scaling: an event without a time and blank reasons read as null", () => {
		const withEvent = toService(
			serviceView({
				scaling: scaling({
					lastScaleEvent: { at: "2026-01-04T00:00:00Z", to: 3 },
					blockedReason: "quota",
					ineligibleReason: "volume",
				}),
			}),
		);
		expect(withEvent.scaling.lastScaleEvent).toEqual({
			at: "2026-01-04T00:00:00Z",
			to: 3,
		} as never);
		expect(withEvent.scaling.blockedReason).toBe("quota");
		expect(withEvent.scaling.ineligibleReason).toBe("volume");
		const empty = toService(
			serviceView({ scaling: scaling({ lastScaleEvent: { at: "" } }) }),
		);
		expect(empty.scaling.lastScaleEvent).toBeNull();
	});

	test("maps additional processes with their own start command, healthcheck and scaling", () => {
		const service = toService(
			serviceView({
				config: {
					startCommand: ["serve"],
					healthcheck: { type: "tcp" },
					processes: [
						{
							name: "worker",
							startCommand: null,
							healthcheck: { type: "none" },
						},
					],
				},
				processes: [
					{
						name: "web",
						command: null,
						scaling: scaling({ blockedReason: "no capacity" }),
						observed: null,
					},
					{
						name: "worker",
						command: ["run", "jobs"],
						scaling: scaling(),
						observed: { name: "worker", running: 2 },
					},
					{
						name: "idle",
						command: [],
						scaling: scaling(),
						observed: { name: "" },
					},
				],
			}),
		);
		expect(service.config.startCommand).toEqual(["serve"]);
		expect(service.config.processes).toEqual([
			{
				name: "worker",
				startCommand: [],
				healthcheck: { type: "none" },
			} as never,
		]);
		expect(service.processes.map((p) => p.name)).toEqual([
			"web",
			"worker",
			"idle",
		]);
		expect(service.processes[0]?.command).toEqual([]);
		expect(service.processes[0]?.scaling.blockedReason).toBe("no capacity");
		expect(service.processes[0]?.observed).toBeNull();
		expect(service.processes[1]?.command).toEqual(["run", "jobs"]);
		expect(service.processes[1]?.observed).toEqual({
			name: "worker",
			running: 2,
		} as never);
		expect(service.processes[2]?.observed).toBeNull();
	});

	test("fills a domain with the plain-serve defaults when the wire omits its rules", () => {
		const service = toService(
			serviceView({
				domains: [
					{
						id: "dom-1",
						hostname: "example.com",
						targetPort: 8080,
						generated: false,
						status: "active",
						statusMessage: "",
						certificateId: "",
					},
				],
			}),
		);
		expect(service.domains).toEqual([
			{
				...PLAIN_DOMAIN_RULE,
				id: "dom-1",
				hostname: "example.com",
				targetPort: 8080,
				component: "",
				process: "",
				generated: false,
				status: "active",
				statusMessage: null,
				dnsOk: null,
				dnsCheckedAt: null,
				createdAt: null,
				dnsResolver: "",
				cert: null,
				dns: null,
				wwwPartner: "",
				wwwRedirect: false,
				wwwRedirectFor: "",
			},
		]);
	});

	test("keeps the rules a domain does carry", () => {
		const cert = { source: "custom", status: "valid" };
		const dns = { provider: "cloudflare" };
		const service = toService(
			serviceView({
				domains: [
					{
						id: "dom-2",
						hostname: "old.example.com",
						targetPort: 80,
						component: "api",
						process: "web",
						generated: true,
						status: "error",
						statusMessage: "dns not found",
						dnsOk: false,
						dnsCheckedAt: "2026-01-05T00:00:00Z",
						createdAt: "2026-01-01T00:00:00Z",
						dnsResolver: "cloudflare",
						cert,
						dns,
						wwwPartner: "www.old.example.com",
						wwwRedirect: true,
						wwwRedirectFor: "example.com",
						pathPrefix: "/api",
						stripPrefix: true,
						addPrefix: "/v2",
						kind: "redirect",
						redirectTo: "https://new.example.com",
						redirectCode: 301,
						preservePath: false,
						tlsMode: "custom",
						certificateId: "cert-1",
						options: { compress: true },
						authUsers: ["ops"],
					},
				],
			}),
		);
		expect(service.domains[0]).toEqual({
			id: "dom-2",
			hostname: "old.example.com",
			targetPort: 80,
			component: "api",
			process: "web",
			generated: true,
			status: "error",
			statusMessage: "dns not found",
			dnsOk: false,
			dnsCheckedAt: "2026-01-05T00:00:00Z",
			createdAt: "2026-01-01T00:00:00Z",
			dnsResolver: "cloudflare",
			cert,
			dns,
			wwwPartner: "www.old.example.com",
			wwwRedirect: true,
			wwwRedirectFor: "example.com",
			pathPrefix: "/api",
			stripPrefix: true,
			addPrefix: "/v2",
			kind: "redirect",
			redirectTo: "https://new.example.com",
			redirectCode: 301,
			preservePath: false,
			tlsMode: "custom",
			certificateId: "cert-1",
			options: { compress: true },
			authUsers: ["ops"],
		} as never);
	});
});

describe("toServer", () => {
	const base = {
		id: "srv-1",
		name: "edge-1",
		status: "online",
		createdAt: "2026-01-01T00:00:00Z",
		serviceCount: 2,
		drainState: "none",
		drainMessage: "",
		acceptWorkloads: true,
		buildEnabled: false,
		buildMaxConcurrent: 2,
		buildAllowEmulation: false,
		buildAllowPreviews: false,
		controlPlane: false,
		unrestricted: false,
	};

	test("a freshly registered server reads as empty, not undefined", () => {
		const server = toServer(wire<Gen.Server>(base));
		expect(server.publicIp).toBeNull();
		expect(server.baseDomain).toBeNull();
		expect(server.acmeEmail).toBeNull();
		expect(server.agentVersion).toBeNull();
		expect(server.protocolVersion).toBeNull();
		expect(server.lastSeenAt).toBeNull();
		expect(server.facts).toBeNull();
		expect(server.latest).toBeNull();
		expect(server.tunnel).toBeNull();
		expect(server.composeVersion).toBe("");
		expect(server.proxy).toEqual({
			status: "",
			v2: false,
			generation: 0,
			appliedGeneration: 0,
			tailnet: null,
		});
		expect("error" in server.proxy).toBe(false);
	});

	test("carries the proxy state and its error", () => {
		const server = toServer(
			wire<Gen.Server>({
				...base,
				proxy: {
					status: "degraded",
					error: "port 443 busy",
					v2: true,
					generation: 5,
					appliedGeneration: 4,
					tailnet: { state: "up" },
				},
			}),
		);
		expect(server.proxy).toEqual({
			status: "degraded",
			error: "port 443 busy",
			v2: true,
			generation: 5,
			appliedGeneration: 4,
			tailnet: { state: "up" },
		} as never);
	});

	test("facts and the latest sample count only when they carry data", () => {
		expect(
			toServer(
				wire<Gen.Server>({
					...base,
					facts: { os: "" },
					latest: { sampledAt: "" },
				}),
			),
		).toMatchObject({ facts: null, latest: null });
		const facts = { os: "linux", arch: "arm64" };
		const latest = { sampledAt: "2026-01-06T00:00:00Z", cpu: 0.4 };
		const server = toServer(
			wire<Gen.Server>({
				...base,
				facts,
				latest,
				publicIp: "203.0.113.7",
				baseDomain: "apps.example.com",
				acmeEmail: "ops@example.com",
				agentVersion: "1.2.3",
				protocolVersion: 2,
				lastSeenAt: "2026-01-06T00:00:01Z",
				composeVersion: "2.30.1",
				tunnel: { state: "up" },
			}),
		);
		expect(server).toMatchObject({
			facts,
			latest,
			publicIp: "203.0.113.7",
			baseDomain: "apps.example.com",
			acmeEmail: "ops@example.com",
			agentVersion: "1.2.3",
			protocolVersion: 2,
			lastSeenAt: "2026-01-06T00:00:01Z",
			composeVersion: "2.30.1",
			tunnel: { state: "up" },
			serviceCount: 2,
			buildMaxConcurrent: 2,
		});
	});
});

describe("toDeployment", () => {
	const base = {
		id: "dep-1",
		serviceId: "svc-1",
		releaseId: "",
		serverId: "",
		trigger: "manual",
		actorType: "user",
		actorLabel: "Ada",
		status: "succeeded",
		strategy: "recreate",
		statusMessage: "",
		message: "",
		rolledBackFrom: "",
		generation: 2,
		createdAt: "2026-01-01T00:00:00Z",
		release: null,
		waitingFor: null,
		commit: null,
		canceledAtStep: "",
	};

	test("blank ids and strings read as null; missing timestamps as null", () => {
		const deployment = toDeployment(wire<Gen.DeploymentView>(base));
		expect(deployment).toEqual({
			id: "dep-1",
			serviceId: "svc-1",
			releaseId: null,
			serverId: null,
			buildServerId: null,
			trigger: "manual",
			actorType: "user",
			actorLabel: "Ada",
			status: "succeeded",
			strategy: "recreate",
			statusMessage: null,
			message: null,
			rolledBackFrom: null,
			generation: 2,
			createdAt: "2026-01-01T00:00:00Z",
			startedAt: null,
			finishedAt: null,
			release: null,
			waitingFor: null,
			commit: null,
			cancelRequestedAt: null,
			canceledAtStep: null,
		});
	});

	test("reads an unknown status as failed", () => {
		expect(
			toDeployment(wire<Gen.DeploymentView>({ ...base, status: "weird" }))
				.status,
		).toBe("failed");
	});

	test("takes the build server from a deployment, but a rollback has none", () => {
		expect(
			toDeployment(wire<Gen.DeploymentView>({ ...base, buildServerId: "bs-1" }))
				.buildServerId,
		).toBe("bs-1");
		expect(
			toDeployment(wire<Gen.DeploymentView>({ ...base, buildServerId: "" }))
				.buildServerId,
		).toBeNull();
	});

	test("keeps release, wait, and commit only with an id, serverId and sha", () => {
		const empty = toDeployment(
			wire<Gen.DeploymentView>({
				...base,
				release: { id: "" },
				waitingFor: { serverId: "" },
				commit: { sha: "" },
			}),
		);
		expect(empty.release).toBeNull();
		expect(empty.waitingFor).toBeNull();
		expect(empty.commit).toBeNull();

		const full = toDeployment(
			wire<Gen.DeploymentView>({
				...base,
				release: { id: "rel-1" },
				waitingFor: {
					serverId: "srv-1",
					serverName: "edge",
					offlineSince: null,
				},
				commit: { sha: "abc123" },
				startedAt: "2026-01-01T00:01:00Z",
				finishedAt: "2026-01-01T00:02:00Z",
				cancelRequestedAt: "2026-01-01T00:01:30Z",
				canceledAtStep: "build",
				rolledBackFrom: "dep-0",
			}),
		);
		expect(full.release).toEqual({ id: "rel-1" } as never);
		expect(full.waitingFor).toEqual({
			serverId: "srv-1",
			serverName: "edge",
			offlineSince: null,
		});
		expect(full.commit).toEqual({ sha: "abc123" } as never);
		expect(full).toMatchObject({
			startedAt: "2026-01-01T00:01:00Z",
			finishedAt: "2026-01-01T00:02:00Z",
			cancelRequestedAt: "2026-01-01T00:01:30Z",
			canceledAtStep: "build",
			rolledBackFrom: "dep-0",
		});
	});
});

describe("toVariable", () => {
	const base = {
		key: "DATABASE_URL",
		isSecret: false,
		computedSecret: false,
		source: "user",
		scope: "service",
		version: 3,
		comment: "",
		generated: null,
		references: null,
		rotateEveryDays: null,
		ageDays: 12,
		rotationDue: false,
		updatedAt: "2026-01-01T00:00:00Z",
	};

	test("shows a plain value and defaults a missing value to empty", () => {
		expect(
			toVariable(wire<Gen.Variable>({ ...base, value: "postgres://x" })).value,
		).toBe("postgres://x");
		expect(toVariable(wire<Gen.Variable>(base)).value).toBe("");
	});

	test("never exposes a secret's value, even when the wire carries one", () => {
		const variable = toVariable(
			wire<Gen.Variable>({ ...base, isSecret: true, value: "hunter2" }),
		);
		expect(variable.value).toBeUndefined();
		expect(variable.isSecret).toBe(true);
	});

	test("null generator and reference lists become empty arrays; the problem is kept", () => {
		const variable = toVariable(
			wire<Gen.Variable>({ ...base, problem: "unresolved reference" }),
		);
		expect(variable.generated).toEqual([]);
		expect(variable.references).toEqual([]);
		expect(variable.problem).toBe("unresolved reference");
		expect(variable.version).toBe(3);
		expect(variable.ageDays).toBe(12);
	});
});

describe("toPlan", () => {
	const base = {
		strategy: "recreate",
		reasons: null,
		warnings: null,
		changes: null,
		blocking: null,
		source: { type: "image" },
		server: null,
		placement: null,
		mesh: null,
	};

	test("a bare plan has empty lists and no server, placement or mesh", () => {
		expect(toPlan(wire<Gen.Plan>(base))).toEqual({
			strategy: "recreate",
			reasons: [],
			warnings: [],
			changes: [],
			blocking: [],
			source: { type: "image" },
			server: null,
			placement: null,
			mesh: null,
		} as never);
	});

	test("keeps the server only when it has an id", () => {
		expect(
			toPlan(wire<Gen.Plan>({ ...base, server: { id: "" } })).server,
		).toBeNull();
		expect(
			toPlan(wire<Gen.Plan>({ ...base, server: { id: "srv-1", name: "edge" } }))
				.server,
		).toEqual({ id: "srv-1", name: "edge" } as never);
	});

	test("reads the placement, and falls back to the plan's warnings that concern placement", () => {
		const plan = toPlan(
			wire<Gen.Plan>({
				...base,
				warnings: ["Build will fall back to the target server", "unrelated"],
				placement: { location: "build_server", buildServerId: "bs-1" },
			}),
		);
		expect(plan.placement).toMatchObject({
			location: "build_server",
			buildServerId: "bs-1",
			warnings: [
				{ code: "", message: "Build will fall back to the target server" },
			],
		});
	});

	test("a mesh blocker the plan lists under blocking is shown only there", () => {
		const plan = toPlan(
			wire<Gen.Plan>({
				...base,
				blocking: [{ code: "tailnet_off", message: "Tailscale is off" }],
				mesh: {
					crossServer: true,
					links: null,
					devicesAdded: 0,
					blockers: [
						{ code: "tailnet_off", message: "Tailscale is off" },
						{ code: "quota", message: "Device quota reached" },
					],
				},
			}),
		);
		expect(plan.blocking).toHaveLength(1);
		expect(plan.mesh?.blockers).toEqual([
			{ code: "quota", message: "Device quota reached" },
		] as never);
		expect(plan.mesh?.links).toEqual([]);
	});

	test("a mesh with nothing to say is dropped", () => {
		const plan = toPlan(
			wire<Gen.Plan>({
				...base,
				mesh: { crossServer: false, links: [], blockers: [], devicesAdded: 0 },
			}),
		);
		expect(plan.mesh).toBeNull();
	});
});

describe("canvas conversion", () => {
	test("an empty saved layout becomes empty nodes and groups with no viewport", () => {
		expect(
			toCanvasDocument(
				wire<Gen.Canvas>({
					version: 0,
					layout: { nodes: null, groups: null, viewport: null },
				}),
			),
		).toEqual({
			version: 0,
			layout: { nodes: {}, groups: [], viewport: null },
		});
	});

	test("groups with a null member list read as empty, others keep their members and the viewport", () => {
		const document = toCanvasDocument(
			wire<Gen.Canvas>({
				version: 7,
				layout: {
					nodes: { a: { x: 1, y: 2 } },
					groups: [
						{
							id: "g1",
							name: "Data",
							x: 0,
							y: 0,
							w: 10,
							h: 10,
							collapsed: false,
							serviceIds: null,
						},
						{
							id: "g2",
							name: "Apps",
							x: 5,
							y: 5,
							w: 20,
							h: 20,
							collapsed: true,
							serviceIds: ["a"],
						},
					],
					viewport: { x: 3, y: 4, zoom: 2 },
				},
			}),
		);
		expect(document.version).toBe(7);
		expect(document.layout.nodes).toEqual({ a: { x: 1, y: 2 } });
		expect(document.layout.groups.map((g) => g.serviceIds)).toEqual([
			[],
			["a"],
		]);
		expect(document.layout.viewport).toEqual({ x: 3, y: 4, zoom: 2 });
	});

	test("fromCanvasLayout writes the wire shape and defaults a missing viewport to the origin at zoom 1", () => {
		const layout = {
			nodes: { a: { x: 1, y: 2 } },
			groups: [],
		};
		expect(fromCanvasLayout(layout)).toEqual({
			nodes: { a: { x: 1, y: 2 } },
			groups: [],
			viewport: { x: 0, y: 0, zoom: 1 },
		});
		expect(fromCanvasLayout({ ...layout, viewport: null }).viewport).toEqual({
			x: 0,
			y: 0,
			zoom: 1,
		});
		expect(
			fromCanvasLayout({ ...layout, viewport: { x: 9, y: 8, zoom: 0.5 } })
				.viewport,
		).toEqual({ x: 9, y: 8, zoom: 0.5 });
	});
});

describe("productionFirst", () => {
	test("lists production first, then the rest alphabetically, without mutating the input", () => {
		const input = [
			{ name: "staging", isProduction: false },
			{ name: "dev", isProduction: false },
			{ name: "main", isProduction: true },
		];
		const sorted = productionFirst(input);
		expect(sorted.map((e) => e.name)).toEqual(["main", "dev", "staging"]);
		expect(input.map((e) => e.name)).toEqual(["staging", "dev", "main"]);
	});

	test("an empty list stays empty", () => {
		expect(productionFirst([])).toEqual([]);
	});
});
