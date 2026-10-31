import type { DeploymentStatus, Service } from "#/api/models";
import type { StatusKind } from "#/components/ui/status-icon";
import { relativeTime } from "#/lib/format";

/**
 * The filter-chip vocabulary: what a card's state is, coarsely. Every card
 * falls in exactly one group.
 */
export type StatusGroup =
	| "deployed"
	| "in_flight"
	| "waiting"
	| "failed"
	| "rolled_back"
	| "stopped"
	| "not_deployed";

export const STATUS_GROUP_LABEL: Record<StatusGroup, string> = {
	deployed: "Deployed",
	in_flight: "Deploying",
	waiting: "Waiting",
	failed: "Failed",
	rolled_back: "Rolled back",
	stopped: "Stopped",
	not_deployed: "Not deployed",
};

export interface NodeStatus {
	group: StatusGroup;
	/** The card's status line, e.g. "Deployed 2m ago via GitHub". */
	label: string;
	/** Drives the status icon (see `StatusIcon`). */
	kind: StatusKind;
	/**
	 * The latest deployment is on its way (queued through verifying) and its
	 * server can take it: the card wears the beam and the cables to it carry
	 * a bead. The label stays as the static cue.
	 */
	inFlight: boolean;
}

const IN_FLIGHT: Partial<Record<DeploymentStatus, true>> = {
	created: true,
	queued: true,
	preparing: true,
	building: true,
	releasing: true,
	verifying: true,
};

const IN_FLIGHT_LABEL: Record<string, string> = {
	created: "Queued",
	queued: "Queued",
	preparing: "Preparing",
	building: "Building",
	releasing: "Releasing",
	verifying: "Verifying",
};

/** "GitHub", "GitLab", "Git", or "image" for the "via …" suffix. */
export function sourceLabel(
	service: Pick<Service, "sourceType" | "config">,
): string {
	if (service.sourceType === "external") return "route";
	if (service.sourceType === "image") return "image";
	const url = service.config.gitUrl ?? "";
	if (/github\.com/i.test(url)) return "GitHub";
	if (/gitlab\./i.test(url)) return "GitLab";
	if (/bitbucket\./i.test(url)) return "Bitbucket";
	return "Git";
}

function serverUnreachable(service: Service): boolean {
	const status = service.server?.status;
	return status === "offline" || status === "revoked";
}

/** The latest deployment is still on its way (and not parked on a dead server). */
export function isInFlight(service: Service): boolean {
	const latest = service.latestDeployment;
	return (
		latest !== null &&
		IN_FLIGHT[latest.status] === true &&
		!serverUnreachable(service)
	);
}

/**
 * A deployment can be canceled at any step until the new release is live
 * (queued through verifying). Before the agent started it the cancel is
 * immediate; after, the agent aborts and unwinds. The control plane has the
 * last word (409 `cannot_cancel`).
 */
export function canCancel(service: Service): boolean {
	const status = service.latestDeployment?.status;
	return (
		status === "created" ||
		status === "queued" ||
		status === "preparing" ||
		status === "building" ||
		status === "releasing" ||
		status === "verifying"
	);
}

/**
 * The card's status from the service resource alone. The control plane owns
 * the truth; this only chooses words and a glyph for it.
 */
export function nodeStatus(service: Service): NodeStatus {
	const latest = service.latestDeployment;
	const serving = service.currentDeploymentId !== null;

	if (latest && IN_FLIGHT[latest.status]) {
		if (serverUnreachable(service) && service.server) {
			const since = relativeTime(service.server.lastSeenAt);
			return {
				group: "waiting",
				label: since
					? `Waiting for ${service.server.name} (offline since ${since})`
					: `Waiting for ${service.server.name}`,
				kind: "offline",
				inFlight: false,
			};
		}
		const label = IN_FLIGHT_LABEL[latest.status] ?? "Deploying";
		const building =
			latest.status === "building" ||
			latest.status === "preparing" ||
			latest.status === "created" ||
			latest.status === "queued";
		return {
			group: "in_flight",
			label,
			kind: building ? "building" : "deploying",
			inFlight: true,
		};
	}

	if (service.desiredState === "stopped") {
		return {
			group: "stopped",
			label: "Stopped",
			kind: "stopped",
			inFlight: false,
		};
	}

	if (latest?.status === "failed") {
		return {
			group: "failed",
			label: serving ? "Failed — previous release serving" : "Failed",
			kind: "failed",
			inFlight: false,
		};
	}
	if (latest?.status === "rollback_failed") {
		return {
			group: "failed",
			label: "Rollback failed",
			kind: "failed",
			inFlight: false,
		};
	}
	if (latest?.status === "rolled_back") {
		return {
			group: "rolled_back",
			label: "Rolled back",
			kind: "degraded",
			inFlight: false,
		};
	}
	if (latest?.status === "canceled" && !serving) {
		return {
			group: "not_deployed",
			label: "Canceled",
			kind: "stopped",
			inFlight: false,
		};
	}

	// Succeeded, or canceled while an earlier release still serves.
	if (serving || latest?.status === "succeeded") {
		const observed = service.observed;
		const via = sourceLabel(service);
		if (observed && observed.state !== "running") {
			return {
				group: "failed",
				label:
					observed.state === "missing"
						? "Container missing"
						: `Not running (${observed.state})`,
				kind: "failed",
				inFlight: false,
			};
		}
		if (observed?.health === "unhealthy" || observed?.health === "degraded") {
			return {
				group: "deployed",
				label: observed.health === "degraded" ? "Degraded" : "Unhealthy",
				kind: "degraded",
				inFlight: false,
			};
		}
		const when = relativeTime(
			latest?.status === "succeeded" ? latest.finishedAt : null,
		);
		return {
			group: "deployed",
			label: when ? `Deployed ${when} via ${via}` : `Deployed via ${via}`,
			kind: "live",
			inFlight: false,
		};
	}

	return {
		group: "not_deployed",
		label: "Not deployed",
		kind: "unknown",
		inFlight: false,
	};
}

// --- the LED phase strip ----------------------------------------------------

export type Phase = "build" | "release" | "verify";
export const PHASES: readonly Phase[] = ["build", "release", "verify"];

export const PHASE_LABEL: Record<Phase, string> = {
	build: "Build",
	release: "Release",
	verify: "Verify",
};

/** One LED: unlit, lit and pulsing (this phase is running), done or failed. */
export type PhaseState = "off" | "live" | "done" | "fail";
export type PhaseStates = readonly [PhaseState, PhaseState, PhaseState];

const OFF: PhaseStates = ["off", "off", "off"];
const ALL_DONE: PhaseStates = ["done", "done", "done"];

const STATUS_PHASE: Partial<Record<DeploymentStatus, Phase>> = {
	created: "build",
	queued: "build",
	preparing: "build",
	building: "build",
	releasing: "release",
	verifying: "verify",
};

/** The LED a deployment status lights while it runs. */
export function phaseOfStatus(status: DeploymentStatus): Phase | null {
	return STATUS_PHASE[status] ?? null;
}

const STEP_PHASE: Record<string, Phase> = {
	pull: "build",
	build: "build",
	create: "release",
	start: "release",
	predeploy: "release",
	health: "verify",
	route: "verify",
	verify: "verify",
};

/**
 * The LED a failed deploy step belongs to: the agent's steps map onto the
 * deployment statuses the same way (`pull`/`build` while building,
 * `create`/`start` while releasing, `health`/`route`/`verify` while verifying).
 */
export function phaseOfStep(step: string | null | undefined): Phase | null {
	if (!step) return null;
	return STEP_PHASE[step.toLowerCase()] ?? null;
}

/**
 * The three LEDs for a run that is at `current`: the ones before it are done,
 * `current` is `live` (a running phase) or `fail` (a phase that stopped the
 * run), the ones after are unlit.
 */
export function phaseStates(
	current: Phase,
	mode: "live" | "fail",
): PhaseStates {
	const at = PHASES.indexOf(current);
	return PHASES.map((_, index) =>
		index < at ? "done" : index === at ? mode : "off",
	) as unknown as PhaseStates;
}

/** "0:42", "12:05", "1:02:03": a running clock. */
export function formatElapsed(ms: number): string {
	const total = Math.max(0, Math.floor(ms / 1000));
	const hours = Math.floor(total / 3600);
	const minutes = Math.floor((total % 3600) / 60);
	const seconds = total % 60;
	const ss = seconds.toString().padStart(2, "0");
	if (hours > 0) return `${hours}:${minutes.toString().padStart(2, "0")}:${ss}`;
	return `${minutes}:${ss}`;
}

export type LineTone = "ok" | "accent" | "bad" | "warn" | "muted";

/** Everything a service card's head and LED strip need. */
export interface CardStatus {
	/** The status icon's kind (see `StatusIcon`). */
	kind: StatusKind;
	/** The pill's word: Live, Building, Deploying, Failed, Stopped, Offline, Degraded, Not deployed. */
	pill: string;
	phases: PhaseStates;
	/** The running phase, while a deployment is in flight. */
	phase: Phase | null;
	/** The strip's caption: "Releasing · 0:42", "Live · 2h ago", "Failed at Verify". */
	line: string;
	lineTone: LineTone;
	/** The sentence for tooltips and screen readers (nodeStatus's label). */
	detail: string;
	inFlight: boolean;
	/** ISO time the caption's clock counts from; null when it does not tick. */
	clockFrom: string | null;
	/** The agent is running the pre-deploy command (inside the Release phase). */
	preDeploy: boolean;
}

/**
 * The card's pill, LED strip and caption. `now` only feeds the in-flight
 * clock, so a test can pin it; relative times ("2h ago") read the wall clock.
 */
export function cardStatus(
	service: Service,
	now: number = Date.now(),
): CardStatus {
	const base = nodeStatus(service);
	const latest = service.latestDeployment;
	const serving = service.currentDeploymentId !== null;
	const detail = base.label;
	const make = (
		partial: Omit<
			CardStatus,
			"detail" | "inFlight" | "clockFrom" | "phase" | "preDeploy"
		> &
			Partial<
				Pick<CardStatus, "phase" | "clockFrom" | "inFlight" | "preDeploy">
			>,
	): CardStatus => ({
		preDeploy: false,
		phase: null,
		clockFrom: null,
		inFlight: false,
		detail,
		...partial,
	});

	if (latest && IN_FLIGHT[latest.status]) {
		if (!base.inFlight && service.server) {
			return make({
				kind: "offline",
				pill: "Offline",
				phases: OFF,
				line: `Waiting for ${service.server.name}`,
				lineTone: "warn",
			});
		}
		const preDeploy = latest.currentStep === "predeploy";
		const phase =
			(preDeploy ? phaseOfStep(latest.currentStep) : null) ??
			phaseOfStatus(latest.status) ??
			"build";
		const started = Date.parse(latest.createdAt);
		const elapsed = Number.isNaN(started)
			? ""
			: ` · ${formatElapsed(now - started)}`;
		const building = phase === "build";
		return make({
			kind: building ? "building" : "deploying",
			pill: building ? "Building" : "Deploying",
			phases: phaseStates(phase, "live"),
			phase,
			preDeploy,
			line: `${IN_FLIGHT_LABEL[latest.status] ?? "Deploying"}${elapsed}`,
			lineTone: "accent",
			inFlight: true,
			clockFrom: Number.isNaN(started) ? null : latest.createdAt,
		});
	}

	if (service.desiredState === "stopped") {
		return make({
			kind: "stopped",
			pill: "Stopped",
			phases: OFF,
			line: "Stopped",
			lineTone: "muted",
		});
	}

	if (latest?.status === "failed" || latest?.status === "rollback_failed") {
		const phase = phaseOfStep(latest.failedStep);
		const rollback = latest.status === "rollback_failed";
		const at = phase ?? (rollback ? "verify" : null);
		return make({
			kind: "failed",
			pill: "Failed",
			phases: at ? phaseStates(at, "fail") : OFF,
			line: rollback
				? "Rollback failed"
				: phase
					? latest.failedStep?.toLowerCase() === "predeploy"
						? "Failed at Pre-deploy"
						: `Failed at ${PHASE_LABEL[phase]}`
					: "Failed",
			lineTone: "bad",
		});
	}
	if (latest?.status === "rolled_back") {
		return make({
			kind: "degraded",
			pill: "Degraded",
			phases: phaseStates(phaseOfStep(latest.failedStep) ?? "verify", "fail"),
			line: "Rolled back",
			lineTone: "warn",
		});
	}

	if (serving || latest?.status === "succeeded") {
		if (service.server && serverUnreachable(service)) {
			const since = relativeTime(service.server.lastSeenAt);
			return make({
				kind: "offline",
				pill: "Offline",
				phases: ALL_DONE,
				line: since ? `Offline · seen ${since}` : "Server offline",
				lineTone: "warn",
			});
		}
		const observed = service.observed;
		if (observed && observed.state !== "running") {
			return make({
				kind: "failed",
				pill: "Failed",
				phases: ["done", "done", "fail"],
				line:
					observed.state === "missing"
						? "Container missing"
						: `Not running (${observed.state})`,
				lineTone: "bad",
			});
		}
		if (service.maintenance?.enabled) {
			const since = relativeTime(service.maintenance.since);
			return make({
				kind: "warning",
				pill: "Maintenance",
				phases: ALL_DONE,
				line: since ? `503 page · ${since}` : "503 page",
				lineTone: "warn",
			});
		}
		const replicasDown =
			observed !== null &&
			observed.replicasDesired > 0 &&
			observed.replicasReady < observed.replicasDesired;
		if (
			observed?.health === "unhealthy" ||
			observed?.health === "degraded" ||
			replicasDown
		) {
			return make({
				kind: "degraded",
				pill: "Degraded",
				phases: ALL_DONE,
				line: replicasDown
					? `${observed?.replicasReady}/${observed?.replicasDesired} replicas ready`
					: observed?.health === "unhealthy"
						? "Unhealthy"
						: "Degraded",
				lineTone: "warn",
			});
		}
		const when = relativeTime(
			latest?.status === "succeeded" ? latest.finishedAt : null,
		);
		return make({
			kind: "live",
			pill: "Live",
			phases: ALL_DONE,
			line: when ? `Live · ${when}` : "Live",
			lineTone: "ok",
		});
	}

	return make({
		pill: "Not deployed",
		kind: "unknown",
		phases: OFF,
		line: latest?.status === "canceled" ? "Canceled" : "Not deployed",
		lineTone: "muted",
	});
}

// --- memory -----------------------------------------------------------------

export type MemoryTone = "ok" | "warn" | "bad";

/** Used share of the limit, clamped to 0..1; null without a positive limit. */
export function memoryFraction(used: number, limit: number): number | null {
	if (!(limit > 0) || !Number.isFinite(used) || used < 0) return null;
	return Math.min(1, used / limit);
}

/** The memory bar's colour: calm, amber from 75%, red from 90%. */
export function memoryTone(fraction: number): MemoryTone {
	if (fraction >= 0.9) return "bad";
	if (fraction >= 0.75) return "warn";
	return "ok";
}
