import type { Scaling, Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";

type ProcessView = Service["processes"][number];

/** A scaling block of one running replica that may scale; override what the case cares about. */
export function makeScaling(over: Partial<Scaling> = {}): Scaling {
	return {
		replicas: 1,
		ready: 1,
		autoscaling: { enabled: false, min: 1, max: 3, targetCpuPercent: 70 },
		lastScaleEvent: null,
		blockedReason: null,
		eligible: true,
		ineligibleReason: null,
		...over,
	};
}

/** A healthy running process of the service view; override what the case cares about. */
export function makeProcess(
	over: Partial<ProcessView> & { name: string },
): ProcessView {
	const scaling = over.scaling ?? makeScaling();
	return {
		default: false,
		port: 0,
		command: ["bin/jobs"],
		singleton: false,
		stopGraceSeconds: 0,
		pending: false,
		removed: false,
		observed: {
			name: over.name,
			state: "running",
			health: "healthy",
			replicasDesired: scaling.replicas,
			replicasReady: scaling.ready,
		},
		scaling,
		...over,
	};
}

/** A service that runs `web` (default, port 3000) and `worker`, healthy, plus what the case overrides. */
export function makeProcessService(over: Partial<Service> = {}): Service {
	return makeService({
		id: "svc-shop",
		name: "shop",
		slug: "shop",
		processes: [
			makeProcess({
				name: "web",
				default: true,
				port: 3000,
				command: ["bin/rails", "server"],
			}),
			makeProcess({ name: "worker" }),
		],
		...over,
	});
}
