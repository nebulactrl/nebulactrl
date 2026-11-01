import type { Service } from "#/api/models";
import { hasProcesses } from "#/features/processes/model";

/** Up to this many replicas each get a pip; more are drawn as one stacked bar. */
export const PIP_LIMIT = 16;

export type ReplicaState = "ready" | "starting" | "failed" | "stopped";

/** The order pips are drawn in, and the legend lists the states that occur. */
export const REPLICA_STATES: readonly ReplicaState[] = [
	"ready",
	"starting",
	"failed",
	"stopped",
];

/** "Not running" covers both a stopped replica and one that was never placed. */
export const REPLICA_STATE_LABEL: Record<ReplicaState, string> = {
	ready: "Ready",
	starting: "Starting",
	failed: "Failed",
	stopped: "Not running",
};

/** What the agent observed of one replica. */
export interface ObservedReplica {
	index: number;
	state: string;
	health: string;
	/** The process the replica runs; "" or absent for a single-process service. */
	process?: string;
}

export interface ReplicaStripInput {
	/** Replicas wanted: one slot each. */
	wanted: number;
	/** Per-replica observation; null or empty when the agent sent none. */
	replicas: readonly ObservedReplica[] | null;
	/** Replicas ready, used only when there is no per-replica observation. */
	ready: number;
	/** The service is stopped on purpose: nothing is failed, everything is stopped. */
	stopped: boolean;
}

export interface ReplicaPipModel {
	/** The replica's 0-based index (inside its process, for a service with several). */
	index: number;
	state: ReplicaState;
	/** Why, in a few words: "Unhealthy", "Exited", "Not placed". */
	detail: string;
	/** The process the replica belongs to; absent for a single-process service. */
	process?: string;
}

export interface ReplicaStripModel {
	total: number;
	/** `pips` up to the limit, one `bar` above it. */
	mode: "pips" | "bar";
	counts: Record<ReplicaState, number>;
	/** Ready first, then starting, failed, not running. Empty in `bar` mode. */
	pips: ReplicaPipModel[];
	/** Only the states that occur, in pip order. */
	legend: { state: ReplicaState; label: string; count: number }[];
	/** "2 of 2 replicas ready", plus what the rest are doing. */
	summary: string;
}

function classify(
	replica: ObservedReplica,
	stopped: boolean,
): Pick<ReplicaPipModel, "state" | "detail"> {
	const { state, health } = replica;
	if (state === "running") {
		if (health === "healthy") return { state: "ready", detail: "Healthy" };
		if (health === "unhealthy") return { state: "failed", detail: "Unhealthy" };
		return { state: "starting", detail: "Waiting for its health check" };
	}
	if (state === "created") return { state: "starting", detail: "Created" };
	if (stopped) return { state: "stopped", detail: "Stopped" };
	if (state === "exited") return { state: "failed", detail: "Exited" };
	if (state === "restarting") return { state: "failed", detail: "Restarting" };
	if (state === "dead") return { state: "failed", detail: "Dead" };
	return { state: "stopped", detail: state ? `Stopped (${state})` : "Stopped" };
}

function collect(input: ReplicaStripInput, total: number): ReplicaPipModel[] {
	const { replicas, stopped } = input;
	if (replicas && replicas.length > 0) {
		// A replica past the wanted count is being scaled in: it has no slot.
		const byIndex = new Map<number, ReplicaPipModel>();
		for (const replica of replicas) {
			if (replica.index < 0 || replica.index >= total) continue;
			byIndex.set(replica.index, {
				index: replica.index,
				...classify(replica, stopped),
			});
		}
		return Array.from(
			{ length: total },
			(_, index): ReplicaPipModel =>
				byIndex.get(index) ?? { index, state: "stopped", detail: "Not placed" },
		);
	}
	const ready = stopped ? 0 : Math.min(total, Math.max(0, input.ready));
	return Array.from(
		{ length: total },
		(_, index): ReplicaPipModel =>
			index < ready
				? { index, state: "ready", detail: "Healthy" }
				: {
						index,
						state: "stopped",
						detail: stopped ? "Stopped" : "Not placed",
					},
	);
}

function describe(total: number, counts: Record<ReplicaState, number>): string {
	if (total === 0) return "No replicas";
	const lead = `${counts.ready} of ${total} ${total === 1 ? "replica" : "replicas"} ready`;
	const rest = [
		counts.starting > 0 ? `${counts.starting} starting` : null,
		counts.failed > 0 ? `${counts.failed} failed` : null,
		counts.stopped > 0 ? `${counts.stopped} not running` : null,
	].filter((part): part is string => part !== null);
	return [lead, ...rest].join(", ");
}

/** How many pips each process wants and what the agent says of it. */
export interface ProcessReplicaInput {
	process: string;
	/** The default process also owns replicas the agent reports with no process. */
	isDefault: boolean;
	wanted: number;
	ready: number;
}

function build(
	all: ReplicaPipModel[],
	total: number,
	order: (a: ReplicaPipModel, b: ReplicaPipModel) => number,
): ReplicaStripModel {
	const counts: Record<ReplicaState, number> = {
		ready: 0,
		starting: 0,
		failed: 0,
		stopped: 0,
	};
	for (const pip of all) counts[pip.state] += 1;
	const mode = total > PIP_LIMIT ? "bar" : "pips";
	return {
		total,
		mode,
		counts,
		pips: mode === "pips" ? all.sort(order) : [],
		legend: REPLICA_STATES.filter((state) => counts[state] > 0).map(
			(state) => ({
				state,
				label: REPLICA_STATE_LABEL[state],
				count: counts[state],
			}),
		),
		summary: describe(total, counts),
	};
}

const rank = (state: ReplicaState) => REPLICA_STATES.indexOf(state);

/**
 * Group a service's replicas by state for the replica strip: one slot per
 * wanted replica, counted and ordered ready, starting, failed, not running
 * (by index within a state), and the way to draw them: pips up to
 * `PIP_LIMIT`, one stacked bar above it.
 */
export function groupReplicas(input: ReplicaStripInput): ReplicaStripModel {
	const total = Math.max(0, Math.floor(input.wanted));
	return build(
		collect(input, total),
		total,
		(a, b) => rank(a.state) - rank(b.state) || a.index - b.index,
	);
}

/**
 * The replicas of a service that runs several processes: each process has
 * its own slots (indexed inside the process), listed in the order the
 * processes are given, and by state then index within one.
 */
export function groupProcessReplicas(input: {
	processes: readonly ProcessReplicaInput[];
	replicas: readonly ObservedReplica[] | null;
	stopped: boolean;
}): ReplicaStripModel {
	const position = new Map(
		input.processes.map((entry, at) => [entry.process, at]),
	);
	let total = 0;
	const all: ReplicaPipModel[] = [];
	for (const entry of input.processes) {
		const wanted = Math.max(0, Math.floor(entry.wanted));
		total += wanted;
		const own = (input.replicas ?? []).filter((replica) => {
			const owner = replica.process ?? "";
			return owner === entry.process || (entry.isDefault && owner === "");
		});
		for (const pip of collect(
			{ wanted, replicas: own, ready: entry.ready, stopped: input.stopped },
			wanted,
		)) {
			all.push({ ...pip, process: entry.process });
		}
	}
	return build(
		all,
		total,
		(a, b) =>
			(position.get(a.process ?? "") ?? 0) -
				(position.get(b.process ?? "") ?? 0) ||
			rank(a.state) - rank(b.state) ||
			a.index - b.index,
	);
}

/**
 * The strip of a service: its wanted replicas against what the agent
 * observed. A service with processes lists every process' replicas, or only
 * `process`' when one is named.
 */
export function replicaStrip(
	service: Service,
	process?: string,
): ReplicaStripModel {
	const stopped = service.desiredState === "stopped";
	if (!hasProcesses(service)) {
		return groupReplicas({
			wanted: service.scaling.replicas,
			replicas: service.observed?.replicas ?? null,
			ready: service.observed?.replicasReady ?? service.scaling.ready,
			stopped,
		});
	}
	const named = service.processes.some((entry) => entry.name === process);
	return groupProcessReplicas({
		processes: service.processes
			.filter((entry) => !entry.removed && (!named || entry.name === process))
			.map((entry) => ({
				process: entry.name,
				isDefault: entry.default,
				wanted: entry.scaling.replicas,
				ready: entry.observed?.replicasReady ?? entry.scaling.ready,
			})),
		replicas: service.observed?.replicas ?? null,
		stopped,
	});
}
