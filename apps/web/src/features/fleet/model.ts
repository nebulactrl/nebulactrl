// Pure helpers behind the fleet view and the drain sheet: capacity math,
// schedulability and the progress of a drain. No React, no network.

import type {
	DrainPlan,
	DrainView,
	FleetServer,
	FleetService,
	Move,
	PlanMove,
	Server,
} from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";

export type DrainState = Server["drainState"];

/** `used / total` in percent, or null when the total is unknown. */
export function percentOf(used: number, total: number): number | null {
	return total > 0 ? (used / total) * 100 : null;
}

export interface MemoryMeter {
	/** Used memory in percent of the host; null before the first heartbeat. */
	usedPercent: number | null;
	/** Where the committed-limits marker sits, clamped to the track (0–100). */
	committedMarkerPercent: number | null;
	/** Committed limits in percent of the host, unclamped (can exceed 100). */
	committedPercent: number | null;
	/** The memory limits of the placed services add up to more than the host. */
	overcommitted: boolean;
}

/** The numbers behind a server's memory bar and its overcommit warning. */
export function memoryMeter(
	server: Pick<
		FleetServer,
		"memUsedBytes" | "memTotalBytes" | "committedMemBytes"
	>,
): MemoryMeter {
	const committedPercent =
		server.committedMemBytes > 0
			? percentOf(server.committedMemBytes, server.memTotalBytes)
			: null;
	return {
		usedPercent: percentOf(server.memUsedBytes, server.memTotalBytes),
		committedMarkerPercent:
			committedPercent === null ? null : Math.min(100, committedPercent),
		committedPercent,
		overcommitted:
			server.memTotalBytes > 0 &&
			server.committedMemBytes > server.memTotalBytes,
	};
}

/** The sentence under an overcommitted server: how far past the host it is. */
export function overcommitText(
	server: Pick<FleetServer, "memTotalBytes" | "committedMemBytes">,
	format: (bytes: number) => string,
): string {
	const over = server.committedMemBytes - server.memTotalBytes;
	return `Memory limits add up to ${format(server.committedMemBytes)}, ${format(over)} more than this host has. Services can be killed when they all use their limit.`;
}

/**
 * The servers worth a column: a revoked server with nothing placed on it has
 * no capacity and no services, so it is left out.
 */
export function visibleFleetServers(
	servers: readonly FleetServer[],
): FleetServer[] {
	return servers.filter(
		(server) =>
			server.status !== "revoked" || (server.services ?? []).length > 0,
	);
}

/**
 * A service's memory in percent of its own limit. Null when it has none: a
 * used quantity with no ceiling is a number, not a fill (a meter against the
 * host would sit near zero and read as "empty").
 */
export function serviceMemPercent(
	service: Pick<FleetService, "memBytes" | "memLimitBytes">,
): number | null {
	return service.memLimitBytes > 0
		? percentOf(service.memBytes, service.memLimitBytes)
		: null;
}

/** The scheduling-relevant fields of a server; `acceptWorkloads` absent counts as on. */
type Placeable = Pick<Server, "status" | "drainState"> &
	Partial<Pick<Server, "acceptWorkloads" | "buildEnabled">>;

/** Whether new services may be placed on the server (a draining one, or one that does not accept workloads, is closed). */
export function isSchedulable(server: Placeable): boolean {
	return (
		server.status !== "revoked" &&
		server.status !== "pending" &&
		server.drainState !== "draining" &&
		server.drainState !== "drained" &&
		server.acceptWorkloads !== false
	);
}

/** Why a server cannot take services, in words; null when it can. */
export function unschedulableReason(server: Placeable): string | null {
	if (server.drainState === "draining") return "being drained";
	if (server.drainState === "drained") return "drained";
	if (server.status === "revoked") return "revoked";
	if (server.status === "pending") return "waiting for agent";
	if (server.acceptWorkloads === false) {
		return server.buildEnabled === true
			? "build server"
			: "does not accept workloads";
	}
	return null;
}

export const DRAIN_STATE_LABEL: Record<DrainState, string> = {
	none: "",
	draining: "Draining",
	drained: "Drained",
	failed: "Drain failed",
};

/** Draining can start on a server that is not already draining or revoked. */
export function canStartDrain(
	server: Pick<Server, "status" | "drainState">,
): boolean {
	return (
		server.status !== "revoked" &&
		server.status !== "pending" &&
		server.drainState !== "draining"
	);
}

export interface DrainProgress {
	total: number;
	done: number;
	/** Index of the move in flight, or -1. */
	activeIndex: number;
	/** Index of the failed move, or -1. */
	failedIndex: number;
	/** 0–100. */
	percent: number;
	/** "2 of 5 moved", "Moving api…", "Failed at api". */
	label: string;
}

/** Where a drain stands, from its per-move statuses. */
export function deriveDrainProgress(
	drain: Pick<DrainView, "moves" | "state">,
): DrainProgress {
	const moves = drain.moves ?? [];
	const total = moves.length;
	const done = moves.filter((move) => move.status === "done").length;
	const activeIndex = moves.findIndex(
		(move) => move.status === "moving" || move.status === "deploying",
	);
	const failedIndex = moves.findIndex((move) => move.status === "failed");
	const percent = total === 0 ? 100 : Math.round((done / total) * 100);
	let label = `${done} of ${total} moved`;
	const failed = moves[failedIndex];
	const active = moves[activeIndex];
	if (failed) label = `Failed at ${failed.serviceName}`;
	else if (drain.state === "running" && active)
		label = `Moving ${active.serviceName}… ${done} of ${total} done`;
	else if (drain.state === "completed") label = `All ${total} moved`;
	else if (drain.state === "canceled")
		label = `Canceled after ${done} of ${total}`;
	return { total, done, activeIndex, failedIndex, percent, label };
}

/**
 * What the segmented drain bar draws: one block per move, the finished moves
 * lit. With no moves the bar is a single full track (nothing was left to
 * do), matching the 100% of `deriveDrainProgress`.
 */
export function drainBar(progress: Pick<DrainProgress, "total" | "done">): {
	value: number;
	max: number;
	segments: number | null;
} {
	if (progress.total === 0) return { value: 1, max: 1, segments: null };
	return {
		value: progress.done,
		max: progress.total,
		segments: progress.total,
	};
}

export type MoveTone = "pending" | "active" | "done" | "failed";

export interface MoveAppearance {
	tone: MoveTone;
	label: string;
}

/** One move's status in words ("Deploying to dst", "Moved", "Waiting"). */
export function moveAppearance(move: Move): MoveAppearance {
	switch (move.status) {
		case "done":
			return { tone: "done", label: `Moved to ${move.targetServerName}` };
		case "failed":
			return { tone: "failed", label: "Failed" };
		case "moving":
			return { tone: "active", label: "Changing placement" };
		case "deploying": {
			const stage = move.deploymentStatus ?? "queued";
			return {
				tone: "active",
				label: `Deploying to ${move.targetServerName} (${stage.replace("_", " ")})`,
			};
		}
		default:
			return { tone: "pending", label: "Waiting" };
	}
}

/** What a drain plan needs from the user before it can start. */
export interface PlanReadiness {
	blocking: string[];
	statefulServices: string[];
	/** The plan moves stateful services, so the checkbox is required. */
	needsConfirmation: boolean;
	capacityProblem: boolean;
	empty: boolean;
}

export function planReadiness(plan: DrainPlan): PlanReadiness {
	const moves = plan.moves ?? [];
	const blocking = moves.flatMap((move) =>
		(move.blocking ?? []).map((item) => item.message),
	);
	const statefulServices = moves
		.filter((move) => move.stateful)
		.map((move) => move.serviceName);
	return {
		blocking,
		statefulServices,
		needsConfirmation: statefulServices.length > 0,
		capacityProblem: !plan.capacity.targetFits,
		empty: moves.length === 0,
	};
}

/** Whether the drain may be submitted with the user's confirmation state. */
export function canSubmitDrain(
	readiness: PlanReadiness,
	confirmedStateful: boolean,
): boolean {
	if (readiness.blocking.length > 0) return false;
	if (readiness.needsConfirmation && !confirmedStateful) return false;
	return true;
}

/** The request body of POST /servers/{id}/drain for a plan. */
export function movesFromPlan(
	moves: readonly PlanMove[],
): { serviceId: string; targetServerId: string }[] {
	const out: { serviceId: string; targetServerId: string }[] = [];
	for (const move of moves) {
		if (move.targetServerId) {
			out.push({
				serviceId: move.serviceId,
				targetServerId: move.targetServerId,
			});
		}
	}
	return out;
}

/** A fleet service's state as a status icon kind and a word. */
export function fleetServiceTone(status: FleetService["status"]): {
	kind: StatusKind;
	label: string;
} {
	switch (status) {
		case "running":
			return { kind: "live", label: "Running" };
		case "deploying":
			return { kind: "deploying", label: "Deploying" };
		case "failed":
			return { kind: "failed", label: "Failed" };
		case "unhealthy":
			return { kind: "degraded", label: "Unhealthy" };
		case "missing":
			return { kind: "failed", label: "Container missing" };
		case "stopped":
			return { kind: "stopped", label: "Stopped" };
		case "undeployed":
			return { kind: "pending", label: "Not deployed" };
		default:
			return { kind: "unknown", label: "Unknown" };
	}
}
