import type { SetScalingInputBody } from "#/api/gen/types.gen";
import type { Scaling } from "#/api/models";

/** Server limits: replicas 1–10, CPU target 20–95. */
export const MIN_REPLICAS = 1;
export const MAX_REPLICAS = 10;
export const MIN_TARGET_CPU = 20;
export const MAX_TARGET_CPU = 95;

export type ScalingMode = "manual" | "autoscale";

/** The working copy of the scaling form. */
export interface ScalingFormState {
	mode: ScalingMode;
	replicas: number;
	min: number;
	max: number;
	targetCpuPercent: number;
}

export type ScalingFormErrors = Partial<
	Record<"replicas" | "min" | "max" | "targetCpuPercent", string>
>;

/** The form as the server currently has it. */
export function toFormState(scaling: Scaling): ScalingFormState {
	return {
		mode: scaling.autoscaling.enabled ? "autoscale" : "manual",
		replicas: scaling.replicas,
		min: scaling.autoscaling.min,
		max: scaling.autoscaling.max,
		targetCpuPercent: scaling.autoscaling.targetCpuPercent,
	};
}

function integerInRange(value: number, low: number, high: number): boolean {
	return Number.isInteger(value) && value >= low && value <= high;
}

/** Mirrors the server's validation so a bad value never leaves the browser. */
export function validateScaling(state: ScalingFormState): ScalingFormErrors {
	const errors: ScalingFormErrors = {};
	const range = `${MIN_REPLICAS}–${MAX_REPLICAS}`;
	if (state.mode === "manual") {
		if (!integerInRange(state.replicas, MIN_REPLICAS, MAX_REPLICAS)) {
			errors.replicas = `Replicas must be a whole number from ${range}.`;
		}
		return errors;
	}
	if (!integerInRange(state.min, MIN_REPLICAS, MAX_REPLICAS)) {
		errors.min = `Minimum must be a whole number from ${range}.`;
	}
	if (!integerInRange(state.max, MIN_REPLICAS, MAX_REPLICAS)) {
		errors.max = `Maximum must be a whole number from ${range}.`;
	} else if (!errors.min && state.max < state.min) {
		errors.max = "Maximum must be at least the minimum.";
	}
	if (!integerInRange(state.targetCpuPercent, MIN_TARGET_CPU, MAX_TARGET_CPU)) {
		errors.targetCpuPercent = `Target CPU must be a whole number from ${MIN_TARGET_CPU}–${MAX_TARGET_CPU}%.`;
	}
	return errors;
}

/**
 * The PUT body that moves the server from `scaling` to `state`, or `null`
 * when nothing would change. Switching to manual turns autoscaling off in the
 * same request: the server checks a replica count against the autoscaling
 * setting it is about to store, so one request is enough.
 */
export function buildScalingBody(
	scaling: Scaling,
	state: ScalingFormState,
): SetScalingInputBody | null {
	const current = scaling.autoscaling;
	if (state.mode === "manual") {
		const body: SetScalingInputBody = {};
		if (current.enabled) body.autoscaling = { enabled: false };
		if (state.replicas !== scaling.replicas) body.replicas = state.replicas;
		return body.autoscaling || body.replicas !== undefined ? body : null;
	}
	const unchanged =
		current.enabled &&
		current.min === state.min &&
		current.max === state.max &&
		current.targetCpuPercent === state.targetCpuPercent;
	if (unchanged) return null;
	return {
		autoscaling: {
			enabled: true,
			min: state.min,
			max: state.max,
			targetCpuPercent: state.targetCpuPercent,
		},
	};
}

/** "2/3 replicas · autoscaling 1–3 @ 70% CPU" (the autoscaling part only when on). */
export function scalingSummary(scaling: Scaling): string {
	const noun = scaling.replicas === 1 ? "replica" : "replicas";
	const head = `${scaling.ready}/${scaling.replicas} ${noun}`;
	const as = scaling.autoscaling;
	if (!as.enabled) return head;
	return `${head} · autoscaling ${as.min}–${as.max} @ ${as.targetCpuPercent}% CPU`;
}

/** Live readiness: "2 of 2 replicas ready" or "1 of 2 ready, starting". */
export function readinessText(scaling: Scaling): string {
	const { ready, replicas } = scaling;
	if (ready === replicas) {
		return `${ready} of ${replicas} ${replicas === 1 ? "replica" : "replicas"} ready`;
	}
	return `${ready} of ${replicas} ready, ${ready < replicas ? "starting" : "stopping"}`;
}

/**
 * "Scaled 2 → 3 · CPU 84% > 70% for 2m · autoscaler". The relative time is
 * appended by the view, which keeps it ticking.
 */
export function scaleEventText(
	event: NonNullable<Scaling["lastScaleEvent"]>,
): string {
	const actor =
		event.actor === "system" || !event.actor ? "autoscaler" : event.actor;
	return [`Scaled ${event.from} → ${event.to}`, event.reason, actor]
		.filter((part) => part !== "")
		.join(" · ");
}

/** "Scaling api to 2 replicas" / "Autoscaling api between 1 and 3 replicas". */
export function appliedToast(name: string, state: ScalingFormState): string {
	if (state.mode === "autoscale") {
		return `Autoscaling ${name} between ${state.min} and ${state.max} replicas`;
	}
	return `Scaling ${name} to ${state.replicas} ${state.replicas === 1 ? "replica" : "replicas"}`;
}

// --- processes --------------------------------------------------------------

/** What the scaling helpers read of a service: its own scaling and its processes'. */
export interface ScalingSource {
	name: string;
	scaling: Scaling;
	processes: readonly {
		name: string;
		default: boolean;
		scaling: Scaling;
		removed: boolean;
	}[];
}

/** The processes that can be scaled one by one, default first; none for a single-process service. */
export function scalableProcesses(
	service: Pick<ScalingSource, "processes">,
): { name: string; isDefault: boolean }[] {
	return service.processes
		.filter((process) => !process.removed)
		.map((process) => ({ name: process.name, isDefault: process.default }));
}

/**
 * The process a scale dialog opens on: the one asked for, else the default
 * process, else the first. Only processes that can scale are considered while
 * any can. Undefined for a service without processes.
 */
export function initialScaleProcess(
	service: Pick<ScalingSource, "processes">,
	requested?: string,
): string | undefined {
	const rows = scaleRows(service);
	const scalable = rows.filter((row) => row.eligible);
	const options = scalable.length > 0 ? scalable : rows;
	return (
		options.find((row) => row.name === requested) ??
		options.find((row) => row.isDefault) ??
		options[0]
	)?.name;
}

/**
 * The scaling of one process, which is what the matching process reports for
 * itself: the service's own `scaling.ready` counts every process' replicas.
 * A name the service does not have and a service without processes read the
 * service's own scaling, which is what the legacy endpoint changes.
 */
export function scalingOfProcess(
	service: Pick<ScalingSource, "scaling" | "processes">,
	name?: string,
): Scaling {
	const process = service.processes.find(
		(candidate) => candidate.name === name,
	);
	return process ? process.scaling : service.scaling;
}

/**
 * Whether anything of the service can scale: some process that is not being
 * removed, or the service itself when it has no processes.
 */
export function serviceCanScale(
	service: Pick<ScalingSource, "scaling" | "processes">,
): boolean {
	if (service.processes.length === 0) return service.scaling.eligible;
	return service.processes.some(
		(process) => !process.removed && process.scaling.eligible,
	);
}

/** The default process and a service without processes scale through the service's own endpoint; the others through their own. */
export function usesServiceScaling(
	service: Pick<ScalingSource, "processes">,
	name?: string,
): boolean {
	const process = service.processes.find(
		(candidate) => candidate.name === name,
	);
	return !process || process.default;
}

/** Whether replicas can be changed for the process (its own eligibility, not the service's). */
export function processCanScale(
	service: Pick<ScalingSource, "scaling" | "processes">,
	name?: string,
): boolean {
	return scalingOfProcess(service, name).eligible;
}

/** Who a toast or title speaks of: the service, or "worker of shop" for one of several processes. */
export function scaleSubject(
	service: Pick<ScalingSource, "name" | "processes">,
	name?: string,
): string {
	if (service.processes.length === 0 || name === undefined) return service.name;
	return `${name} of ${service.name}`;
}

/** One line per process for the Scaling section and the scale dialog's picker. */
export interface ScaleRow {
	name: string;
	isDefault: boolean;
	eligible: boolean;
	/** "2/3 replicas · autoscaling 1–3 @ 70% CPU", or what keeps it at one. */
	text: string;
	/** Why the process cannot scale; null when it can. */
	reason: string | null;
}

const SINGLE_INSTANCE = "Runs one instance";

/** The scaling of each process, in service order; none for a single-process service. */
export function scaleRows(
	service: Pick<ScalingSource, "processes">,
): ScaleRow[] {
	return service.processes
		.filter((process) => !process.removed)
		.map((process) => {
			const { scaling } = process;
			return {
				name: process.name,
				isDefault: process.default,
				eligible: scaling.eligible,
				text: scaling.eligible ? scalingSummary(scaling) : SINGLE_INSTANCE,
				reason: scaling.eligible
					? null
					: (scaling.ineligibleReason ??
						"Replicas need a stateless service: no volumes and no files."),
			};
		});
}
