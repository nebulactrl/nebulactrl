// The mesh screens' logic: chips, wording, plan merging and the canvas badges.
// Pure, so every sentence the screens say is tested here and the components
// only draw what this decides.

import type {
	AclDiff,
	Rule as AclRule,
	EnvironmentMeshView,
	MeshBlocker,
	MeshGrantView,
	MeshLinkView,
	View as MeshView,
	NodeView,
	PeerView,
	RouteView,
	RuleView,
	ServerCheckResult,
	MeshPlan as WireMeshPlan,
	MeshPlanLink as WireMeshPlanLink,
} from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";

export type { MeshBlocker, MeshView };
export type MeshProvider = MeshView["provider"];
/** How NebulaCtrl treats the tailnet's access policy. */
export type AclMode = MeshView["aclMode"];
export type AclState = MeshView["aclState"];
/** A node's or a link's state; `pending` is a cable whose link does not exist yet. */
export type NodeState = NodeView["state"] | MeshLinkView["state"] | "pending";
/** `replication` links carry a database's replicas; `service` links carry apps. */
export type MeshPurpose = MeshLinkView["purpose"];
/** Why a link may reach an exposure: a variable reference, a canvas edge, or a replica. */
export type GrantSource = MeshGrantView["source"];
export type CheckOutcome = ServerCheckResult["outcome"] | (string & {});

/** One link an action needs: the wire link with its provider list never null. */
export type MeshPlanLink = Omit<WireMeshPlanLink, "providers"> & {
	providers: string[];
};

/** The `mesh` block of a deployment plan and of a link preview, lists never null. */
export interface MeshPlan {
	crossServer: boolean;
	links: MeshPlanLink[];
	devicesAdded: number;
	blockers: MeshBlocker[];
}

/**
 * The wire plan as the screens read it. Null when nothing crosses a server
 * (the control plane sends an empty block then), so "no mesh section" is one
 * case, not three.
 */
export function toMeshPlan(
	wire: WireMeshPlan | null | undefined,
): MeshPlan | null {
	if (!wire) return null;
	const links = (wire.links ?? []).map((link) => ({
		...link,
		providers: link.providers ?? [],
	}));
	const blockers = wire.blockers ?? [];
	if (
		!wire.crossServer &&
		links.length === 0 &&
		blockers.length === 0 &&
		wire.devicesAdded === 0
	) {
		return null;
	}
	return {
		crossServer: wire.crossServer || links.length > 0,
		links,
		devicesAdded: wire.devicesAdded,
		blockers,
	};
}

export const PRICING_URL = "https://tailscale.com/pricing";

/** A node or link reported longer ago than this reads "unknown", not "online". */
export const STALE_AFTER_MS = 60_000;

export function plural(n: number, one: string, many = `${one}s`): string {
	return `${n} ${n === 1 ? one : many}`;
}

export interface Chip {
	kind: StatusKind;
	label: string;
}

/**
 * The state of a node or link as one status icon and one word. An online node
 * whose last report is older than a minute is "Unknown": the server may be
 * gone, and a stale "Online" is the lie to avoid.
 */
export function stateChip(
	state: NodeState | string,
	lastSeenAt?: string | null,
	now: number = Date.now(),
): Chip {
	if (state === "online" && lastSeenAt) {
		const seen = Date.parse(lastSeenAt);
		if (Number.isFinite(seen) && now - seen > STALE_AFTER_MS) {
			return { kind: "unknown", label: "Unknown" };
		}
	}
	switch (state) {
		case "online":
			return { kind: "live", label: "Online" };
		case "provisioning":
			return { kind: "deploying", label: "Provisioning" };
		case "pending":
			return { kind: "pending", label: "Waiting" };
		case "offline":
			return { kind: "offline", label: "Offline" };
		case "degraded":
			return { kind: "degraded", label: "Degraded" };
		case "needs_attention":
			return { kind: "failed", label: "Needs attention" };
		default:
			return { kind: "unknown", label: "Unknown" };
	}
}

/** The coordinator (Headscale or Tailscale's control plane) as a chip. */
export function coordinatorChip(coordinator: MeshView["coordinator"]): Chip {
	if (coordinator.ok === true) return { kind: "live", label: "Reachable" };
	if (coordinator.ok === false) {
		return { kind: "failed", label: "Unreachable" };
	}
	return { kind: "unknown", label: "Not checked" };
}

export const ACL_MODES: readonly {
	id: AclMode;
	label: string;
	hint: string;
}[] = [
	{
		id: "advisory",
		label: "Advisory",
		hint: "NebulaCtrl shows the rules it needs and checks connectivity. You paste them into your tailnet's policy.",
	},
	{
		id: "managed",
		label: "Managed",
		hint: "NebulaCtrl writes the rules into the tailnet's policy itself.",
	},
	{
		id: "off",
		label: "Off",
		hint: "No rules are shown or written. The tailnet's own policy decides.",
	},
];

/** The ACL state as a chip. Advisory asks a person to act, so it reads as a warning. */
export function aclChip(mode: AclMode, state: AclState): Chip {
	if (mode === "off") return { kind: "stopped", label: "Off" };
	switch (state) {
		case "applied":
			return { kind: "live", label: "Applied" };
		case "pending":
			return { kind: "pending", label: "Applying" };
		case "drift":
			return { kind: "degraded", label: "Drifted" };
		case "advisory":
			return { kind: "warning", label: "Not written: add the rules yourself" };
		case "error":
			return { kind: "failed", label: "Error" };
		default:
			return { kind: "unknown", label: "Not checked" };
	}
}

/**
 * What the chosen ACL mode means for this provider, in one sentence. Advisory
 * on Tailscale says what a default tailnet allows, so nobody thinks the rules
 * narrow anything before they are added.
 */
export function aclModeNote(
	provider: MeshProvider,
	mode: AclMode,
): string | null {
	if (mode === "off") return null;
	if (mode === "managed") {
		return provider === "headscale"
			? "NebulaCtrl owns the whole policy document. Edits made by hand are overwritten the next time the mesh changes."
			: "Only rules that name mesh node addresses are replaced. Your other rules and comments stay.";
	}
	return provider === "tailscale"
		? "Tailscale's default policy allows everything, so a link can reach any mesh port on the other server until you add these rules. The connectivity check shows what is reachable."
		: "Until the rules are in your policy, a link can reach any mesh port on the other server. The connectivity check shows what is reachable.";
}

/** The outcome of a connectivity check as a chip: what the agent saw when it dialled. */
export function checkChip(outcome: CheckOutcome): Chip {
	switch (outcome) {
		case "connected":
			return { kind: "live", label: "Connected" };
		case "refused":
			return { kind: "failed", label: "Refused" };
		case "timeout":
			return { kind: "failed", label: "Timed out" };
		case "no_path":
			return { kind: "failed", label: "No path" };
		default:
			return { kind: "unknown", label: "Unknown" };
	}
}

/** What enabling mesh on a server does to the access policy, in the mode it is in. */
export function enableAclEffect(mode: AclMode): string {
	switch (mode) {
		case "managed":
			return "Access policy: the rules are regenerated and written to your tailnet's policy.";
		case "advisory":
			return "Access policy: nothing is written. The rules update on the Mesh page for you to copy into your tailnet's policy.";
		default:
			return "Access policy: off. No rules are shown or written; your tailnet's own policy decides.";
	}
}

export function providerLabel(provider: MeshProvider): string {
	switch (provider) {
		case "tailscale":
			return "Tailscale";
		case "headscale":
			return "Headscale";
		default:
			return "Not connected";
	}
}

/**
 * Where fallback relays come from, said plainly. Managed
 * Headscale uses Tailscale's public relay map; a Tailscale tailnet uses
 * Tailscale's own.
 */
export function derpDisclosure(provider: MeshProvider): string {
	const base =
		"When two servers cannot reach each other directly, traffic goes through a relay. Relays carry only encrypted traffic: they see ciphertext and the addresses of both ends, never your data.";
	if (provider === "headscale") {
		return `${base} Managed Headscale falls back to Tailscale's public relay servers (DERP). NebulaCtrl does not run a relay of its own.`;
	}
	if (provider === "tailscale") {
		return `${base} Your tailnet falls back to Tailscale's relay servers (DERP).`;
	}
	return `${base} The fallback is Tailscale's public relay servers (DERP); NebulaCtrl does not run a relay of its own.`;
}

/** "3 peers · 2 direct, 1 relayed · 12 ms" for a node's peers. */
export function peerSummary(peers: readonly PeerView[]): string {
	const online = peers.filter((peer) => peer.online);
	if (peers.length === 0) return "No peers";
	if (online.length === 0)
		return `${plural(peers.length, "peer")}, none online`;
	const direct = online.filter((peer) => peer.direct).length;
	const relayed = online.length - direct;
	const latencies = online
		.map((peer) => peer.latencyMs)
		.filter((ms): ms is number => typeof ms === "number" && ms >= 0);
	const parts: string[] = [];
	if (direct > 0) parts.push(`${direct} direct`);
	if (relayed > 0) parts.push(`${relayed} relayed`);
	let text = `${plural(online.length, "peer")} online · ${parts.join(", ")}`;
	if (latencies.length > 0) {
		text += ` · ${Math.round(Math.min(...latencies))} ms`;
	}
	return text;
}

/** The devices the budget line talks about, with the pricing it links to. */
export function deviceBudgetText(used: number, added = 0): string {
	const adds = added > 0 ? `This change adds ${plural(added, "device")}. ` : "";
	return `${adds}This team uses ${plural(used, "tagged device")}. Tailscale Personal includes 50 tagged resources and charges $1 a month for each one more; every mesh node is tagged. Headscale has no device limit.`;
}

export interface RuleRow {
	id: string;
	/** "production · web-02 → db-01". */
	from: string;
	/** "100.64.0.7:15432". */
	target: string;
	because: string;
	purpose?: MeshPurpose;
}

/** The derived rules as rows a person can read: the link by name, the target by address. */
export function ruleRows(
	rules: readonly RuleView[],
	links: readonly MeshLinkView[],
): RuleRow[] {
	const byId = new Map(links.map((link) => [link.id, link]));
	return rules.map((rule, index) => {
		const link = byId.get(rule.linkId);
		return {
			id: `${rule.linkId}:${rule.dst}:${rule.port}:${index}`,
			from: link ? linkLabel(link) : rule.src,
			target: rule.dst,
			because: rule.because,
			...(link ? { purpose: link.purpose } : {}),
		};
	});
}

/**
 * The difference between the generated policy and the tailnet's stored one, as
 * diff lines: `+ accept <src> -> <dst>` is missing from the tailnet, `-` is a
 * NebulaCtrl rule the tailnet still holds that is no longer generated. Empty
 * when there is nothing to change.
 */
export function aclDiffText(diff: AclDiff): string {
	const line = (sign: "+" | "-", rule: AclRule) =>
		`${sign} ${rule.action} ${(rule.src ?? []).join(", ")} -> ${(rule.dst ?? []).join(", ")}`;
	return [
		...(diff.add ?? []).map((rule) => line("+", rule)),
		...(diff.remove ?? []).map((rule) => line("-", rule)),
	].join("\n");
}

/** "production · web-02 → db-01". */
export function linkLabel(link: MeshLinkView): string {
	return `${link.environmentName} · ${link.serverName} → ${link.remoteServerName}`;
}

export function purposeLabel(purpose: MeshPurpose): string {
	return purpose === "replication" ? "Replication" : "Services";
}

/** Why a service reaches another over the mesh, in the words of the canvas. */
export function grantSourceLabel(source: GrantSource | (string & {})): string {
	switch (source) {
		case "edge":
			return "canvas link";
		case "replica":
			return "replica";
		default:
			return "variable reference";
	}
}

/** What a grant lets a link reach, as one phrase. */
export function grantLabel(grant: MeshGrantView): string {
	const target = `${grant.providerSlug}:${grant.meshPort}`;
	switch (grant.source) {
		case "replica":
			return `${target} for replica ${grant.refLabel || "of this database"}`;
		case "edge":
			return `${target} by a link${grant.refLabel ? ` from ${grant.refLabel}` : ""}`;
		default:
			return `${target} by a reference${grant.refLabel ? ` in ${grant.refLabel}` : ""}`;
	}
}

// --- Plans: a deploy, a canvas link, a replica ------------------------------

export function linkPlanLabel(link: MeshPlanLink): string {
	return `${link.fromServerName} → ${link.toServerName}`;
}

/**
 * One plan out of several (a link preview per consumer): the union of the
 * links, a device counted once per new link, blockers de-duplicated by code
 * and server. Null when nothing crosses a server.
 */
export function mergePlans(
	plans: ReadonlyArray<MeshPlan | null | undefined>,
): MeshPlan | null {
	const real = plans.filter((plan): plan is MeshPlan => Boolean(plan));
	const crossing = real.filter(
		(plan) => plan.crossServer || plan.links.length > 0,
	);
	if (crossing.length === 0) return null;
	const links = new Map<string, MeshPlanLink>();
	const blockers = new Map<string, MeshBlocker>();
	for (const plan of crossing) {
		for (const link of plan.links) {
			// The same pair is one link for every consumer that needs it.
			const key = `${link.fromServerId}>${link.toServerId}:${link.purpose}`;
			const known = links.get(key);
			if (!known) {
				links.set(key, link);
			} else {
				links.set(key, {
					...known,
					providers: [...new Set([...known.providers, ...link.providers])],
				});
			}
		}
		for (const blocker of plan.blockers) {
			blockers.set(`${blocker.code}:${blocker.serverId ?? ""}`, blocker);
		}
	}
	const fromLinks = [...links.values()].filter(
		(link) => link.state === "new",
	).length;
	const reported = Math.max(...crossing.map((plan) => plan.devicesAdded));
	return {
		crossServer: true,
		links: [...links.values()],
		// A new link is a device. When the server listed no links, believe its number.
		devicesAdded: links.size > 0 ? fromLinks : reported,
		blockers: [...blockers.values()],
	};
}

/** True when going ahead adds devices, so the person must say so. */
export function needsAcceptance(plan: MeshPlan | null | undefined): boolean {
	return Boolean(plan) && (plan?.devicesAdded ?? 0) > 0;
}

/** True when the plan stops the action. */
export function isBlocked(plan: MeshPlan | null | undefined): boolean {
	return (plan?.blockers.length ?? 0) > 0;
}

/** The one line above a plan's links. Null when nothing crosses a server. */
export function planHeadline(plan: MeshPlan | null | undefined): string | null {
	if (!plan || (!plan.crossServer && plan.links.length === 0)) return null;
	if (plan.blockers.length > 0)
		return "These servers cannot connect over the mesh yet";
	if (plan.devicesAdded > 0) {
		return `Connect over the mesh: adds ${plural(plan.devicesAdded, "device")}`;
	}
	return "Connect over the mesh: uses links that already exist, adds no device";
}

/** What to do about a blocker, where the code says. Falls back to the message. */
export function blockerAction(blocker: MeshBlocker): {
	text: string;
	serverId?: string;
} | null {
	switch (blocker.code) {
		case "mesh_server_required":
			return {
				text: blocker.serverName
					? `Enable mesh on ${blocker.serverName}`
					: "Enable mesh on this server",
				...(blocker.serverId ? { serverId: blocker.serverId } : {}),
			};
		case "mesh_not_configured":
			return { text: "Connect Tailscale or set up Headscale first" };
		case "mesh_no_tun":
			return blocker.serverId
				? {
						text: "See the server's mesh status",
						serverId: blocker.serverId,
					}
				: null;
		default:
			return null;
	}
}

/** Error codes of the mesh API in the sentence a person needs. */
const ERROR_TEXT: Record<string, string> = {
	mesh_not_configured:
		"Connect Tailscale in Team settings → Integrations, or set up Headscale under Private network (Headscale), before using the mesh.",
	mesh_agent_too_old:
		"The agent on this server is too old for the mesh. Upgrade it, then try again.",
	mesh_no_tun:
		"This server has no /dev/net/tun, so it cannot host a link to another server. Servers can still join the mesh.",
	mesh_coordinator_unreachable:
		"The mesh coordinator did not answer. Existing connections keep working; changes wait until it is back.",
	mesh_paused: "The mesh is paused. Changes wait until it resumes.",
	mesh_server_required: "Enable mesh on both servers first.",
	mesh_not_exposable:
		"This service cannot be shared over the mesh: it needs a TCP port, and Compose stacks and routes cannot be shared.",
	mesh_link_not_ready:
		"The mesh link is not ready yet. Nothing was changed; try again in a moment.",
	mesh_confirmation_required:
		"This adds devices to your tailnet. Confirm that you accept them, then try again.",
	mesh_policy_scope_missing:
		"The Tailscale OAuth client has no policy_file scope. Add it in the Tailscale admin console, reconnect, then choose Managed again.",
	mesh_acl_shared_tailnet:
		"Another team uses this tailnet, so NebulaCtrl will not write its policy.",
	integration_in_use: "The mesh uses this integration. Disable the mesh first.",
	mesh_in_use:
		"Servers are still in the mesh. Disable the mesh on each server first.",
	mesh_server_in_use:
		"Replicas or links to other servers still use this server's mesh node. Remove them first, then disable the mesh here.",
	mesh_acl_mode_unsupported:
		"This tailnet cannot take the access policy from NebulaCtrl. Use advisory and add the rules yourself.",
	mesh_tag_not_allowed:
		"That tag is not in the tailnet integration's allowed tags. Add it there first.",
};

export function meshErrorText(
	code: string | undefined,
	fallback: string,
): string {
	return (code ? ERROR_TEXT[code] : undefined) ?? fallback;
}

// --- The canvas: cross-server edges and "via mesh" chips --------------------

export interface MeshEdgeInfo {
	/** "web-02 → db-01". */
	label: string;
	state: NodeState | "pending";
	/** Devices the deploy of the consumer adds for this cable; zero once the link exists. */
	devicesAdded: number;
	purpose: MeshPurpose | null;
}

export interface CanvasLinkInput {
	id: string;
	fromServiceId: string;
	toServiceId: string;
}

export interface CanvasServiceInput {
	id: string;
	server: { id: string; name: string } | null;
}

/** A canvas link between services that run on different servers. */
export function crossServerLinks(
	links: readonly CanvasLinkInput[],
	services: readonly CanvasServiceInput[],
): CanvasLinkInput[] {
	const server = new Map(services.map((s) => [s.id, s.server?.id ?? null]));
	return links.filter((link) => {
		const from = server.get(link.fromServiceId);
		const to = server.get(link.toServiceId);
		return from && to && from !== to;
	});
}

export interface CanvasMesh {
	/** By link id: the mesh badge of a cross-server cable. */
	edges: ReadonlyMap<string, MeshEdgeInfo>;
	/** By consumer service id: the "via mesh" chip. */
	via: ReadonlyMap<string, MeshEdgeInfo & { providers: string[] }>;
}

export const EMPTY_CANVAS_MESH: CanvasMesh = {
	edges: new Map(),
	via: new Map(),
};

const STATE_RANK: Record<string, number> = {
	online: 0,
	provisioning: 1,
	pending: 2,
	degraded: 3,
	unknown: 4,
	offline: 5,
	needs_attention: 6,
};

/** A route's state as a node state: `granted` is a link still coming up. */
export function routeState(state: string): NodeState {
	if (state === "granted") return "provisioning";
	return state in STATE_RANK ? (state as NodeState) : "unknown";
}

/** The worse of two states, so a card never reads healthier than its worst route. */
function worse(a: NodeState | "pending", b: NodeState | "pending") {
	return (STATE_RANK[a] ?? 4) >= (STATE_RANK[b] ?? 4) ? a : b;
}

/**
 * The badges the canvas draws for an environment. A cross-server cable is
 * always badged, as pending until a route exists for it (the link and its
 * device are added by an explicit action, never by drawing the cable); once the
 * environment's mesh lists a route for the pair, the badge carries that route's
 * state and the link's device count.
 */
export function buildCanvasMesh(
	cross: readonly CanvasLinkInput[],
	services: readonly CanvasServiceInput[],
	mesh: EnvironmentMeshView | undefined,
): CanvasMesh {
	if (cross.length === 0) return EMPTY_CANVAS_MESH;
	const byService = new Map(services.map((s) => [s.id, s]));
	const routeFor = (
		consumer: string,
		provider: string,
	): RouteView | undefined =>
		(mesh?.routes ?? []).find(
			(route) => route.consumerId === consumer && route.providerId === provider,
		);
	const edges = new Map<string, MeshEdgeInfo>();
	const via = new Map<string, MeshEdgeInfo & { providers: string[] }>();
	for (const link of cross) {
		const from = byService.get(link.fromServiceId);
		const to = byService.get(link.toServiceId);
		if (!from?.server || !to?.server) continue;
		const route = routeFor(link.fromServiceId, link.toServiceId);
		const info: MeshEdgeInfo = {
			label: `${from.server.name} → ${to.server.name}`,
			state: route ? routeState(route.state) : "pending",
			devicesAdded: route?.devicesAdded ?? 0,
			purpose: "service",
		};
		edges.set(link.id, info);
		// The card says "via mesh" once a route is past waiting for its link.
		if (!route || info.state === "pending") continue;
		const known = via.get(link.fromServiceId);
		via.set(link.fromServiceId, {
			label: known ? `${known.label}, ${info.label}` : info.label,
			state: known ? worse(known.state, info.state) : info.state,
			devicesAdded: (known?.devicesAdded ?? 0) + info.devicesAdded,
			purpose: info.purpose,
			providers: [...(known?.providers ?? []), route.provider],
		});
	}
	return { edges, via };
}

/** The sentence a "via mesh" chip carries as its tooltip. */
export function viaMeshTitle(
	info: MeshEdgeInfo & { providers: string[] },
): string {
	const state = stateChip(info.state).label.toLowerCase();
	return `Reaches ${info.providers.join(", ")} over the mesh (${info.label}). Link ${state}.`;
}
