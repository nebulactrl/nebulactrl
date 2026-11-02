// Fixtures for the scaling component tests: services as the console models
// them (the same shape the API returns once mapped).

import type { Scaling, Service } from "#/api/models";
import { makeObserved, makeService } from "#/features/canvas/lib/fixtures";

export { makeObserved, makeService };

/** The scaling of a service that can scale; override what the case cares about. */
export function scalingOf(over: Partial<Scaling> = {}): Scaling {
	return { ...makeService().scaling, ...over };
}

/** One process of a service, scaling to `replicas` with none ready yet. */
export function processEntry(
	name: string,
	over: Partial<Service["processes"][number]> = {},
	replicas = 1,
): Service["processes"][number] {
	return {
		name,
		default: name === "web",
		port: 0,
		command: [],
		singleton: false,
		stopGraceSeconds: 30,
		scaling: scalingOf({ replicas, ready: 0 }),
		observed: null,
		pending: false,
		removed: false,
		...over,
	};
}

/** A running, healthy replica the agent reports for `process`. */
export function runningReplica(process: string, index: number) {
	return {
		component: "",
		index,
		process,
		containerId: `${process}-${index}`,
		state: "running",
		health: "healthy",
		startedAt: null,
		restartCount: 0,
	};
}
