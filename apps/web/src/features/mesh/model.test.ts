import { describe, expect, test } from "bun:test";
import type {
	EnvironmentMeshView,
	MeshLinkView,
	PeerView,
	RouteView,
	MeshPlan as WireMeshPlan,
} from "#/api/gen/types.gen";
import {
	aclChip,
	aclDiffText,
	aclModeNote,
	blockerAction,
	buildCanvasMesh,
	checkChip,
	coordinatorChip,
	crossServerLinks,
	derpDisclosure,
	deviceBudgetText,
	enableAclEffect,
	grantLabel,
	grantSourceLabel,
	isBlocked,
	linkLabel,
	type MeshPlan,
	type MeshPlanLink,
	mergePlans,
	meshErrorText,
	needsAcceptance,
	peerSummary,
	planHeadline,
	plural,
	routeState,
	ruleRows,
	stateChip,
	toMeshPlan,
	viaMeshTitle,
} from "./model";

const NOW = Date.parse("2026-10-27T12:00:00Z");
const ago = (ms: number) => new Date(NOW - ms).toISOString();

const peer = (patch: Partial<PeerView> = {}): PeerView => ({
	name: "web-02",
	ips: ["100.64.0.2"],
	direct: true,
	online: true,
	...patch,
});

const planLink = (patch: Partial<MeshPlanLink> = {}): MeshPlanLink => ({
	fromServerId: "s-a",
	fromServerName: "web-02",
	toServerId: "s-b",
	toServerName: "db-01",
	purpose: "service",
	state: "new",
	providers: ["api"],
	...patch,
});

const plan = (patch: Partial<MeshPlan> = {}): MeshPlan => ({
	crossServer: true,
	links: [planLink()],
	devicesAdded: 1,
	blockers: [],
	...patch,
});

describe("chips", () => {
	test("an online node is Online while its report is fresh", () => {
		expect(stateChip("online", ago(10_000), NOW)).toEqual({
			kind: "live",
			label: "Online",
		});
	});

	test("an online node not heard from for over a minute is Unknown, never Online", () => {
		expect(stateChip("online", ago(61_000), NOW)).toEqual({
			kind: "unknown",
			label: "Unknown",
		});
	});

	test("an offline node stays Offline however old the report", () => {
		expect(stateChip("offline", ago(600_000), NOW).label).toBe("Offline");
	});

	test("every state names itself in words", () => {
		const labels = [
			"provisioning",
			"pending",
			"degraded",
			"needs_attention",
			"wat",
		].map((state) => stateChip(state).label);
		expect(labels).toEqual([
			"Provisioning",
			"Waiting",
			"Degraded",
			"Needs attention",
			"Unknown",
		]);
	});

	test("the coordinator is reachable, unreachable or not checked", () => {
		const at = "2026-10-28T10:00:00Z";
		expect(coordinatorChip({ ok: true, checkedAt: at }).label).toBe(
			"Reachable",
		);
		expect(coordinatorChip({ ok: false, checkedAt: at }).kind).toBe("failed");
		expect(coordinatorChip({ ok: null, checkedAt: null }).label).toBe(
			"Not checked",
		);
	});

	test("advisory asks a person to act, so it is a warning and not a success", () => {
		expect(aclChip("advisory", "advisory").kind).toBe("warning");
		expect(aclChip("managed", "applied").kind).toBe("live");
		expect(aclChip("managed", "drift").label).toBe("Drifted");
		expect(aclChip("off", "applied")).toEqual({
			kind: "stopped",
			label: "Off",
		});
	});
});

describe("peerSummary", () => {
	test("no peers", () => {
		expect(peerSummary([])).toBe("No peers");
	});

	test("counts direct and relayed among the online peers, with the best latency", () => {
		expect(
			peerSummary([
				peer({ latencyMs: 12.4 }),
				peer({ name: "b", direct: false, latencyMs: 80 }),
				peer({ name: "c", online: false }),
			]),
		).toBe("2 peers online · 1 direct, 1 relayed · 12 ms");
	});

	test("peers that are all offline say so", () => {
		expect(peerSummary([peer({ online: false })])).toBe("1 peer, none online");
	});
});

describe("wording", () => {
	test("plural", () => {
		expect(plural(1, "device")).toBe("1 device");
		expect(plural(2, "device")).toBe("2 devices");
		expect(plural(0, "tagged device")).toBe("0 tagged devices");
	});

	test("the device budget says what it adds and what the team already uses", () => {
		const text = deviceBudgetText(7, 2);
		expect(text).toContain("adds 2 devices");
		expect(text).toContain("uses 7 tagged devices");
		expect(text).toContain("50 tagged resources");
		expect(deviceBudgetText(7)).not.toContain("adds");
	});

	test("the relay disclosure names Tailscale's public relays for managed Headscale", () => {
		expect(derpDisclosure("headscale")).toContain(
			"Tailscale's public relay servers",
		);
		expect(derpDisclosure("headscale")).toContain("ciphertext");
		expect(derpDisclosure("none")).toContain("DERP");
	});

	test("error codes read as a sentence, an unknown one keeps the server's words", () => {
		expect(meshErrorText("mesh_no_tun", "x")).toContain("/dev/net/tun");
		expect(meshErrorText("mesh_paused", "x")).toContain("paused");
		expect(meshErrorText("nope", "the server said")).toBe("the server said");
		expect(meshErrorText(undefined, "fallback")).toBe("fallback");
	});

	test("nothing in the copy claims high availability or automatic failover", () => {
		const everything = [
			derpDisclosure("headscale"),
			derpDisclosure("tailscale"),
			deviceBudgetText(3, 1),
			...[
				"mesh_not_configured",
				"mesh_agent_too_old",
				"mesh_no_tun",
				"mesh_coordinator_unreachable",
				"mesh_paused",
				"mesh_server_required",
				"mesh_not_exposable",
				"mesh_link_not_ready",
				"mesh_confirmation_required",
				"mesh_policy_scope_missing",
				"mesh_acl_shared_tailnet",
				"integration_in_use",
			].map((code) => meshErrorText(code, "")),
		].join(" ");
		const claim = /\b(HA|high availability|failover)\b/i; // never allowed in copy
		expect(claim.test(everything)).toBe(false);
	});
});

describe("access rules", () => {
	const link: MeshLinkView = {
		id: "l1",
		environmentId: "e1",
		environmentName: "production",
		projectName: "Shop",
		hostname: "nc-e1-web-02-to-db-01",
		message: "",
		needsReprovision: false,
		serverId: "s-a",
		serverName: "web-02",
		remoteServerId: "s-b",
		remoteServerName: "db-01",
		purpose: "replication",
		state: "online",
		ips: ["100.64.0.9"],
		grants: [],
	};

	test("a rule reads as the link by name and the target by address", () => {
		const [row] = ruleRows(
			[
				{
					src: "100.64.0.9",
					dst: "100.64.0.7:15432",
					port: 15432,
					linkId: "l1",
					because: "replica shop-db-2 of shop-db",
				},
			],
			[link],
		);
		expect(row?.from).toBe("production · web-02 → db-01");
		expect(row?.target).toBe("100.64.0.7:15432");
		expect(row?.because).toBe("replica shop-db-2 of shop-db");
		expect(row?.purpose).toBe("replication");
	});

	test("a rule whose link is gone falls back to the source address", () => {
		const [row] = ruleRows(
			[
				{
					src: "100.64.0.9",
					dst: "100.64.0.7:80",
					port: 80,
					linkId: "x",
					because: "",
				},
			],
			[link],
		);
		expect(row?.from).toBe("100.64.0.9");
	});

	test("two rules between the same servers stay two rows", () => {
		const rule = {
			src: "100.64.0.9",
			dst: "100.64.0.7:5432",
			port: 5432,
			linkId: "l1",
			because: "",
		};
		const rows = ruleRows([rule, { ...rule, port: 8080 }], [link]);
		expect(new Set(rows.map((row) => row.id)).size).toBe(2);
	});

	test("link and grant labels", () => {
		expect(linkLabel(link)).toBe("production · web-02 → db-01");
		expect(
			grantLabel({
				exposureId: "x",
				providerSlug: "postgres",
				refId: "r",
				refLabel: "shop-db-2",
				source: "replica",
				meshPort: 15001,
			}),
		).toBe("postgres:15001 for replica shop-db-2");
		expect(
			grantLabel({
				exposureId: "x",
				providerSlug: "api",
				refId: "r",
				refLabel: "web",
				source: "edge",
				meshPort: 8080,
			}),
		).toBe("api:8080 by a link from web");
		expect(
			grantLabel({
				exposureId: "x",
				providerSlug: "api",
				refId: "r",
				refLabel: "",
				source: "reference",
				meshPort: 8080,
			}),
		).toBe("api:8080 by a reference");
	});
});

describe("plans", () => {
	test("a plan that adds a device says how many and needs acceptance", () => {
		expect(planHeadline(plan())).toBe("Connect over the mesh: adds 1 device");
		expect(planHeadline(plan({ devicesAdded: 2 }))).toBe(
			"Connect over the mesh: adds 2 devices",
		);
		expect(needsAcceptance(plan())).toBe(true);
	});

	test("a plan over links that exist adds nothing and needs no acceptance", () => {
		const existing = plan({
			devicesAdded: 0,
			links: [planLink({ state: "online" })],
		});
		expect(planHeadline(existing)).toContain("adds no device");
		expect(needsAcceptance(existing)).toBe(false);
	});

	test("a blocked plan says so before anything about devices", () => {
		const blocked = plan({
			blockers: [{ code: "mesh_server_required", message: "x" }],
		});
		expect(planHeadline(blocked)).toContain("cannot connect over the mesh yet");
		expect(isBlocked(blocked)).toBe(true);
	});

	test("a plan with nothing crossing a server has no headline", () => {
		expect(
			planHeadline(plan({ crossServer: false, links: [], devicesAdded: 0 })),
		).toBeNull();
		expect(planHeadline(null)).toBeNull();
		expect(needsAcceptance(null)).toBe(false);
		expect(isBlocked(undefined)).toBe(false);
	});

	test("merging counts one device for one link shared by two consumers", () => {
		const merged = mergePlans([
			plan({ links: [planLink({ providers: ["api"] })], devicesAdded: 1 }),
			plan({ links: [planLink({ providers: ["cache"] })], devicesAdded: 1 }),
		]);
		expect(merged?.devicesAdded).toBe(1);
		expect(merged?.links).toHaveLength(1);
		expect(merged?.links[0]?.providers).toEqual(["api", "cache"]);
	});

	test("merging two different links counts two devices; an existing link counts none", () => {
		const merged = mergePlans([
			plan(),
			plan({
				links: [planLink({ toServerId: "s-c", toServerName: "db-02" })],
			}),
			plan({
				links: [planLink({ fromServerId: "s-z", state: "online" })],
				devicesAdded: 0,
			}),
		]);
		expect(merged?.links).toHaveLength(3);
		expect(merged?.devicesAdded).toBe(2);
	});

	test("merging de-duplicates blockers by code and server", () => {
		const blocker = {
			code: "mesh_server_required",
			message: "x",
			serverId: "s-b",
		};
		const merged = mergePlans([
			plan({ blockers: [blocker] }),
			plan({ blockers: [blocker, { ...blocker, serverId: "s-a" }] }),
		]);
		expect(merged?.blockers).toHaveLength(2);
	});

	test("merging same-server plans gives nothing", () => {
		expect(
			mergePlans([
				plan({ crossServer: false, links: [], devicesAdded: 0 }),
				undefined,
				null,
			]),
		).toBeNull();
	});

	test("merging trusts the reported count when the server listed no links", () => {
		expect(
			mergePlans([plan({ links: [], devicesAdded: 2 })])?.devicesAdded,
		).toBe(2);
	});

	test("a blocker that has a way forward names it", () => {
		expect(
			blockerAction({
				code: "mesh_server_required",
				message: "x",
				serverId: "s-b",
				serverName: "db-01",
			}),
		).toEqual({ text: "Enable mesh on db-01", serverId: "s-b" });
		expect(
			blockerAction({ code: "mesh_not_configured", message: "x" })?.text,
		).toContain("Tailscale");
		expect(blockerAction({ code: "who_knows", message: "x" })).toBeNull();
	});
});

describe("the canvas", () => {
	const services = [
		{ id: "web", server: { id: "s-a", name: "web-02" } },
		{ id: "api", server: { id: "s-b", name: "db-01" } },
		{ id: "cache", server: { id: "s-a", name: "web-02" } },
		{ id: "loose", server: null },
	];
	const route = (patch: Partial<RouteView> = {}): RouteView => ({
		consumerId: "web",
		consumerSlug: "web",
		providerId: "api",
		provider: "api",
		server: { id: "s-b", name: "db-01" },
		alias: "api",
		port: 8080,
		state: "online",
		source: "edge",
		sources: ["edge"],
		keys: null,
		devicesAdded: 0,
		...patch,
	});
	const cable = [{ id: "1", fromServiceId: "web", toServiceId: "api" }];

	test("only links between different servers are cross-server", () => {
		const links = [
			{ id: "1", fromServiceId: "web", toServiceId: "api" },
			{ id: "2", fromServiceId: "web", toServiceId: "cache" },
			{ id: "3", fromServiceId: "web", toServiceId: "loose" },
		];
		expect(crossServerLinks(links, services).map((l) => l.id)).toEqual(["1"]);
	});

	test("a cross-server cable is badged as pending before the environment lists a route", () => {
		const canvas = buildCanvasMesh(cable, services, undefined);
		expect(canvas.edges.get("1")).toEqual({
			label: "web-02 → db-01",
			state: "pending",
			devicesAdded: 0,
			purpose: "service",
		});
		expect(canvas.via.size).toBe(0);
	});

	test("a route waiting for its link shows the device it will add, and no card chip yet", () => {
		const canvas = buildCanvasMesh(cable, services, {
			grants: [],
			links: [],
			routes: [route({ state: "pending", devicesAdded: 1 })],
		});
		expect(canvas.edges.get("1")?.devicesAdded).toBe(1);
		expect(canvas.edges.get("1")?.state).toBe("pending");
		expect(canvas.via.has("web")).toBe(false);
	});

	test("an online route gives the badge its state and the card its chip", () => {
		const canvas = buildCanvasMesh(cable, services, {
			grants: [],
			links: [],
			routes: [route()],
		});
		expect(canvas.edges.get("1")?.state).toBe("online");
		expect(canvas.via.get("web")?.providers).toEqual(["api"]);
	});

	test("a consumer of two providers reads as the worse of their states", () => {
		const two = [
			...services,
			{ id: "db", server: { id: "s-c", name: "db-02" } },
		];
		const mesh: EnvironmentMeshView = {
			grants: [],
			links: [],
			routes: [
				route(),
				route({
					providerId: "db",
					provider: "db",
					state: "offline",
					server: { id: "s-c", name: "db-02" },
				}),
			],
		};
		const canvas = buildCanvasMesh(
			[
				{ id: "1", fromServiceId: "web", toServiceId: "api" },
				{ id: "2", fromServiceId: "web", toServiceId: "db" },
			],
			two,
			mesh,
		);
		const info = canvas.via.get("web");
		expect(info?.state).toBe("offline");
		expect(info?.providers).toEqual(["api", "db"]);
		expect(info && viaMeshTitle(info)).toBe(
			"Reaches api, db over the mesh (web-02 → db-01, web-02 → db-02). Link offline.",
		);
	});

	test("an environment with no cross-server cable has no badges", () => {
		const canvas = buildCanvasMesh([], services, undefined);
		expect(canvas.edges.size).toBe(0);
		expect(canvas.via.size).toBe(0);
	});
});

describe("modes, checks and notes", () => {
	test("an ACL mode explains itself for the provider", () => {
		expect(aclModeNote("headscale", "managed")).toContain("whole policy");
		expect(aclModeNote("tailscale", "managed")).toContain("comments stay");
		expect(aclModeNote("tailscale", "advisory")).toContain("allows everything");
		expect(aclModeNote("headscale", "advisory")).toContain("any mesh port");
		expect(aclModeNote("tailscale", "off")).toBeNull();
	});

	test("a connectivity check reads as what the agent saw", () => {
		expect(checkChip("connected")).toEqual({
			kind: "live",
			label: "Connected",
		});
		expect(checkChip("timeout").label).toBe("Timed out");
		expect(checkChip("refused").kind).toBe("failed");
		expect(checkChip("no_path").label).toBe("No path");
		expect(checkChip("???").kind).toBe("unknown");
	});

	test("enabling says what the access policy will do in each mode", () => {
		expect(enableAclEffect("managed")).toContain("written");
		expect(enableAclEffect("advisory")).toContain("nothing is written");
		expect(enableAclEffect("off")).toContain("off");
	});

	test("why a service reaches another, in the canvas's words", () => {
		expect(grantSourceLabel("edge")).toBe("canvas link");
		expect(grantSourceLabel("replica")).toBe("replica");
		expect(grantSourceLabel("reference")).toBe("variable reference");
	});

	test("a granted route is a link still coming up", () => {
		expect(routeState("granted")).toBe("provisioning");
		expect(routeState("online")).toBe("online");
		expect(routeState("wat")).toBe("unknown");
	});
});

describe("the wire plan", () => {
	const wire = (patch: Partial<WireMeshPlan> = {}): WireMeshPlan => ({
		crossServer: false,
		devicesAdded: 0,
		links: null,
		blockers: null,
		...patch,
	});

	test("an empty block is nothing crossing a server", () => {
		expect(toMeshPlan(wire())).toBeNull();
		expect(toMeshPlan(undefined)).toBeNull();
		expect(toMeshPlan(null)).toBeNull();
	});

	test("a full block keeps its links and blockers, nulls become lists", () => {
		const plan = toMeshPlan(
			wire({
				crossServer: true,
				devicesAdded: 1,
				links: [
					{
						fromServerId: "a",
						fromServerName: "web-02",
						toServerId: "b",
						toServerName: "db-01",
						purpose: "service",
						state: "new",
						providers: null,
					},
				],
				blockers: [
					{ code: "mesh_server_required", message: "x", serverId: "b" },
				],
			}),
		);
		expect(plan?.links[0]?.providers).toEqual([]);
		expect(plan?.blockers[0]).toEqual({
			code: "mesh_server_required",
			message: "x",
			serverId: "b",
		});
		expect(plan?.crossServer).toBe(true);
	});

	test("a blocker alone is still a plan", () => {
		const plan = toMeshPlan(
			wire({ blockers: [{ code: "mesh_no_tun", message: "x" }] }),
		);
		expect(plan?.blockers).toHaveLength(1);
	});
});

describe("the policy diff", () => {
	test("missing rules are added lines, outdated ones removed lines", () => {
		expect(
			aclDiffText({
				add: [
					{ action: "accept", src: ["100.64.0.9"], dst: ["100.64.0.7:15432"] },
				],
				remove: [
					{ action: "accept", src: ["100.64.0.8"], dst: ["100.64.0.7:15000"] },
				],
			}),
		).toBe(
			"+ accept 100.64.0.9 -> 100.64.0.7:15432\n- accept 100.64.0.8 -> 100.64.0.7:15000",
		);
		expect(aclDiffText({ add: null, remove: null })).toBe("");
	});
});
