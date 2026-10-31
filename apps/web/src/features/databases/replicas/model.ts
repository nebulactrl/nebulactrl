import type {
	Problem,
	PromotePlan,
	ReplicaPlan,
	ReplicationView,
} from "#/api/gen/types.gen";
import type { LampTone } from "#/components/ui/lamp";
import { formatBytes } from "#/lib/format";

/** At this much WAL behind, a replica counts as lagging (the control plane's own line). */
export const LAG_LIMIT_BYTES = 64 * 1024 * 1024;

/** An observation older than this is not shown as a fact. */
export const STALE_AFTER_MS = 60_000;

/** The most replicas one primary takes. */
export const MAX_REPLICAS = 5;

export interface StateInfo {
	label: string;
	tone: LampTone;
	/** One sentence for the row: what the state means. */
	text: string;
}

const STATES: Record<string, StateInfo> = {
	pending: {
		label: "Queued",
		tone: "staged",
		text: "Waiting for its turn.",
	},
	preparing: {
		label: "Preparing the primary",
		tone: "building",
		text: "Creating the replication role and the access rule on the primary.",
	},
	cloning: {
		label: "Copying data",
		tone: "building",
		text: "Copying the primary's data with a base backup. It can take a while for a large database.",
	},
	deploying: {
		label: "Starting",
		tone: "deploying",
		text: "Starting the replica container.",
	},
	streaming: {
		label: "Streaming",
		tone: "live",
		text: "Receiving changes from the primary.",
	},
	lagging: {
		label: "Lagging",
		tone: "warn",
		text: "Behind the primary by more than the lag limit.",
	},
	broken: {
		label: "Broken",
		tone: "bad",
		text: "The replica is not receiving changes.",
	},
	needs_rebuild: {
		label: "Needs rebuild",
		tone: "bad",
		text: "The primary no longer holds the WAL this replica needs, or it follows a different primary. Rebuild it.",
	},
	promoting: {
		label: "Promoting",
		tone: "deploying",
		text: "Being promoted to primary.",
	},
	removing: {
		label: "Removing",
		tone: "stopped",
		text: "Dropping its slot and role on the primary.",
	},
	failed: {
		label: "Failed",
		tone: "failed",
		text: "Creating the replica failed.",
	},
	unknown: {
		label: "Unknown",
		tone: "off",
		text: "No recent report from the server.",
	},
};

/** How a replica state reads; a state this build does not know reads as unknown. */
export function stateInfo(state: string): StateInfo {
	return STATES[state] ?? (STATES.unknown as StateInfo);
}

/** The state to show: anything older than a minute is "unknown", not a stale fact. */
export function shownState(
	state: string,
	observedAt: string | null | undefined,
	now: number,
): string {
	if (!isLiveState(state) || observedAt == null) return state;
	return now - Date.parse(observedAt) > STALE_AFTER_MS ? "unknown" : state;
}

/** States that come from observation (the others come from the saga). */
function isLiveState(state: string): boolean {
	return state === "streaming" || state === "lagging" || state === "broken";
}

/** A replica the saga is still working on. */
export function isBusyState(state: string): boolean {
	return (
		state === "pending" ||
		state === "preparing" ||
		state === "cloning" ||
		state === "deploying" ||
		state === "promoting" ||
		state === "removing"
	);
}

/**
 * Promote needs a replica that is receiving changes, or one that is broken
 * because its primary is gone (that is what a forced promote is for).
 */
export function isPromotable(state: string): boolean {
	return state === "streaming" || state === "lagging" || state === "broken";
}

/** Rebuild is offered for a replica that cannot catch up by itself. */
export function isRebuildable(state: string): boolean {
	return state === "broken" || state === "needs_rebuild" || state === "failed";
}

/** The panel polls fast while anything is moving. */
export function replicationPollInterval(
	view: ReplicationView | undefined,
	pollMs: number,
	idleMs: number,
): number {
	if (!view) return idleMs;
	const moving =
		view.promotion?.status === "running" ||
		(view.replicas ?? []).some((row) => isBusyState(row.state)) ||
		(view.self != null && isBusyState(view.self.state));
	return moving ? pollMs : idleMs;
}

/** The WAL position of a lag reading as one phrase: "12.4 MiB behind · 0.4 s". */
export function lagText(lag: {
	lagBytes?: number | null | undefined;
	replayLagMs?: number | null | undefined;
}): string {
	if (lag.lagBytes == null) return "Lag not measured yet";
	const bytes =
		lag.lagBytes === 0 ? "In sync" : `${formatBytes(lag.lagBytes)} behind`;
	if (lag.replayLagMs == null || lag.lagBytes === 0) return bytes;
	return `${bytes} · ${formatLagTime(lag.replayLagMs)}`;
}

/** Replay lag as seconds, or milliseconds below one. */
export function formatLagTime(ms: number): string {
	if (ms < 1000) return `${Math.round(ms)} ms`;
	if (ms < 60_000) return `${(Math.round(ms / 100) / 10).toString()} s`;
	return `${Math.round(ms / 60_000)} min`;
}

/** The bar's fill: lag against the lagging line, 0 to 100. */
export function lagPercent(lagBytes: number | null | undefined): number {
	if (lagBytes == null || lagBytes <= 0) return 0;
	return Math.min(100, (lagBytes / LAG_LIMIT_BYTES) * 100);
}

export interface WalInfo {
	label: string;
	tone: LampTone;
	text: string;
}

const WAL: Record<string, WalInfo> = {
	reserved: {
		label: "WAL reserved",
		tone: "ok",
		text: "The primary keeps all the WAL this replica needs.",
	},
	extended: {
		label: "WAL extended",
		tone: "warn",
		text: "The primary holds more WAL than its normal limit for this replica.",
	},
	unreserved: {
		label: "WAL at risk",
		tone: "bad",
		text: "The primary is about to drop WAL this replica still needs. Reconnect it or it will need a rebuild.",
	},
	lost: {
		label: "WAL lost",
		tone: "bad",
		text: "The primary dropped WAL this replica needed. It must be rebuilt.",
	},
};

/** What the primary's slot says about the replica's WAL; null while there is no reading. */
export function walInfo(status: string | null | undefined): WalInfo | null {
	if (!status) return null;
	return (
		WAL[status] ?? {
			label: `WAL ${status}`,
			tone: "info",
			text: "The status of the replication slot on the primary.",
		}
	);
}

/** The writer and reader keys the replica group adds to every Postgres member. */
export type ExportRole = "writer" | "reader";

export function exportRole(key: string): ExportRole | null {
	if (key.startsWith("DATABASE_WRITE_")) return "writer";
	if (key.startsWith("DATABASE_READ_")) return "reader";
	return null;
}

/**
 * Connection variables in reading order: the service's own first, then the
 * writer keys, then the reader keys. Each group keeps the server's order.
 */
export function orderExports<T extends { key: string }>(
	entries: readonly T[],
): T[] {
	const rank = (entry: T) => {
		const role = exportRole(entry.key);
		return role === "writer" ? 1 : role === "reader" ? 2 : 0;
	};
	return entries
		.map((entry, index) => ({ entry, index }))
		.sort((a, b) => rank(a.entry) - rank(b.entry) || a.index - b.index)
		.map(({ entry }) => entry);
}

export const EXPORT_ROLE_TEXT: Record<ExportRole, string> = {
	writer: "Always the current primary.",
	reader:
		"A read replica the consumer can reach (on its server first); the primary when there is none.",
};

/** Messages of a list of issues, empty when absent. */
export function issueMessages(
	issues: readonly Problem[] | null | undefined,
): string[] {
	return (issues ?? []).map((issue) => issue.message);
}

/** Whether a blocker list holds the code. */
export function hasIssue(
	issues: readonly Problem[] | null | undefined,
	code: string,
): boolean {
	return (issues ?? []).some((issue) => issue.code === code);
}

/** Add replica: Create unlocks with a clean plan and the mesh consent the plan asks for. */
export function canConfirmAdd(
	plan: ReplicaPlan | undefined,
	acceptedMesh: boolean,
): boolean {
	if (plan === undefined) return false;
	if ((plan.blockers ?? []).length > 0) return false;
	return plan.devicesAdded === 0 || acceptedMesh;
}

/** Only Postgres has standby replicas; no other engine shows the panel. */
export function supportsReplicas(engineId: string): boolean {
	return engineId === "postgres";
}

/** The heading of the panel: a standby on another server, not a headline feature. */
export const PANEL_TITLE = "Standby replica";

/** What the panel is for, in one line. */
export const PANEL_PURPOSE =
	"Keeps a copy of this database streaming to another server, ready to promote by hand if that server is lost. Can also take read-only queries.";

/** What it is not, so nobody leans on it as a backup or expects it to take over. */
export const PANEL_CAVEAT =
	"Not a backup, and it does not take over by itself: keep your scheduled backups.";

/** Calm note for a replica placed on the primary's own server. */
export const SAME_SERVER_NOTE =
	"On the same server a replica shares the primary's disk and host. It only helps with read load, not with losing the server.";

/**
 * Whether the chosen server is the primary's own, so the same-server note
 * shows. The plan is authoritative once it has arrived; before that the ids
 * decide, so the note does not flicker in after the plan.
 */
export function isSameServerPick(
	plan: Pick<ReplicaPlan, "serverId" | "sameServer"> | undefined,
	serverId: string,
	primaryServerId: string | null | undefined,
): boolean {
	if (serverId === "") return false;
	if (plan !== undefined && plan.serverId === serverId) return plan.sameServer;
	return primaryServerId != null && serverId === primaryServerId;
}

/**
 * The server the add dialog starts on: another online server that is in the
 * mesh (a replica across servers needs it), else the primary's own server,
 * else the first online one. Empty when nothing is online.
 */
export function defaultReplicaServer(
	online: readonly { id: string }[],
	primaryServerId: string | null | undefined,
	meshed: ReadonlySet<string>,
): string {
	const other = online.find(
		(server) => server.id !== primaryServerId && meshed.has(server.id),
	);
	if (other) return other.id;
	const own = online.find((server) => server.id === primaryServerId);
	return own?.id ?? online[0]?.id ?? "";
}

/**
 * Whether the Advanced disclosure starts open: when the database already has
 * replicas, is one, is a fenced old primary, or a promote is running, so
 * existing state is never hidden. A standalone database starts collapsed.
 */
export function shouldOpenAdvanced(
	view:
		| Pick<ReplicationView, "role" | "replicas" | "promotion">
		| null
		| undefined,
): boolean {
	if (!view) return false;
	if (view.role === "replica" || view.role === "fenced") return true;
	if ((view.replicas ?? []).length > 0) return true;
	return view.promotion?.status === "running";
}

/** The one-line statement of where the replica will run and what that costs. */
export function placementLine(plan: ReplicaPlan): string {
	if (plan.sameServer) {
		return "Same server: it reaches the primary over the environment network. The mesh is not needed.";
	}
	const links = (plan.links ?? []).length;
	const devices =
		plan.devicesAdded === 0
			? "adds no mesh device"
			: `adds ${plan.devicesAdded} mesh ${plan.devicesAdded === 1 ? "device" : "devices"}`;
	return `Another server: replication goes over the mesh (${links === 1 ? "1 link" : `${links} links`}); this ${devices}.`;
}

/** A replica's service name when none is typed: `<primary>-replica`, numbered when taken. */
export function suggestReplicaName(
	primarySlug: string,
	taken: readonly string[],
): string {
	const base = `${primarySlug}-replica`;
	if (!taken.includes(base)) return base;
	for (let n = 2; n <= MAX_REPLICAS + 1; n++) {
		if (!taken.includes(`${base}-${n}`)) return `${base}-${n}`;
	}
	return base;
}

/** What a forced promote loses, in the words the contract fixes. */
export function dataLossText(plan: PromotePlan): string {
	if (plan.dataLossWarning) return plan.dataLossWarning;
	const lag =
		plan.lagBytes == null
			? "unknown"
			: `${formatBytes(plan.lagBytes)}${
					plan.lagObservedAt
						? ` as of ${new Date(plan.lagObservedAt).toLocaleTimeString()}`
						: ""
				}`;
	return `Writes the old primary accepted after the replica's last position are lost: up to the replication lag, which is ${lag}.`;
}

/**
 * Promote confirms with a plan that has no blockers, the replica's slug typed
 * exactly and, for a forced promote, an explicit acceptance of the data loss.
 */
export function canConfirmPromote(
	plan: PromotePlan | undefined,
	typed: string,
	slug: string,
	acceptDataLoss: boolean,
): boolean {
	if (plan === undefined) return false;
	if ((plan.blockers ?? []).length > 0) return false;
	if (typed !== slug) return false;
	return plan.mode === "switchover" || acceptDataLoss;
}

export interface ConsumerGroup {
	server: string;
	consumers: NonNullable<PromotePlan["consumers"]>;
	devicesAdded: number;
}

/** The consumers a promote rewrites, grouped by the server they run on. */
export function groupConsumers(
	consumers: PromotePlan["consumers"],
): ConsumerGroup[] {
	const groups = new Map<string, ConsumerGroup>();
	for (const consumer of consumers ?? []) {
		const server = consumer.serverName ?? "No server";
		const group = groups.get(server) ?? {
			server,
			consumers: [],
			devicesAdded: 0,
		};
		group.consumers.push(consumer);
		group.devicesAdded += consumer.devicesAdded;
		groups.set(server, group);
	}
	return [...groups.values()].sort((a, b) => a.server.localeCompare(b.server));
}

/** The latest promotion as a line of text, or null when there is nothing to say. */
export function promotionLine(
	promotion: ReplicationView["promotion"],
): { tone: "running" | "failed" | "done"; text: string } | null {
	if (!promotion) return null;
	const how = promotion.mode === "forced" ? "forced promote" : "switchover";
	switch (promotion.status) {
		case "running":
			return {
				tone: "running",
				text: promotion.message || `The ${how} is running: ${promotion.step}.`,
			};
		case "failed":
			return {
				tone: "failed",
				text:
					promotion.message ||
					`The ${how} failed at ${promotion.step}. The old primary is untouched unless it says otherwise.`,
			};
		default:
			return {
				tone: "done",
				text: promotion.message || `The ${how} finished.`,
			};
	}
}

export type CardChipKind = "primary" | "replica" | "fenced";

export interface CardChip {
	kind: CardChipKind;
	label: string;
	title: string;
	tone: LampTone;
}

/**
 * The canvas chip of a service in a replica group: a replica names its
 * primary, a primary counts its replicas, a fenced old primary says it must
 * not run as a writer again.
 */
export function cardChip(group: {
	role: CardChipKind;
	replicaCount: number;
	primarySlug?: string | null;
	state?: string | null;
}): CardChip {
	if (group.role === "fenced") {
		return {
			kind: "fenced",
			label: "Fenced",
			title:
				"Fenced: a replica was promoted in its place. It stays stopped; rebuild it as a replica or delete it.",
			tone: "bad",
		};
	}
	if (group.role === "replica") {
		const info = stateInfo(group.state ?? "unknown");
		const of = group.primarySlug
			? `Replica of ${group.primarySlug}`
			: "Replica";
		return {
			kind: "replica",
			label: of,
			title: `${of}: ${info.label.toLowerCase()}`,
			tone: info.tone,
		};
	}
	const count = group.replicaCount;
	return {
		kind: "primary",
		label: count === 1 ? "Primary · 1 replica" : `Primary · ${count} replicas`,
		title: "The writer of a read-replica group.",
		tone: "info",
	};
}
