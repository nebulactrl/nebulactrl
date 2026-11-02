// Test-only builders for the stack tests: a stack service as the API returns it.
import type { ObservedReplicaView } from "#/api/gen/types.gen";
import type { Server, Service } from "#/api/models";
import { makeObserved, makeService } from "#/features/canvas/lib/fixtures";
import type { StackComponent } from "./model";

export const COMPONENTS: StackComponent[] = [
	{ name: "db", index: 0, image: "postgres:16", ports: [5432] },
	{ name: "web", index: 1, image: "nginx:1.27", ports: [80, 8080] },
	{ name: "worker", index: 2, image: "busybox", ports: [] },
];

export function replica(
	index: number,
	component: string,
	over: Partial<ObservedReplicaView> = {},
): ObservedReplicaView {
	return {
		component,
		index,
		process: "",
		containerId: `c-${index}`,
		state: "running",
		health: "healthy",
		startedAt: null,
		restartCount: 0,
		...over,
	};
}

/** A Compose stack service; override what the case cares about. */
export function makeStack(
	over: Partial<Service> & {
		components?: StackComponent[];
		compose?: string;
	} = {},
): Service {
	const { components = COMPONENTS, compose = "services: {}\n", ...rest } = over;
	const base = makeService({
		id: "stack-1",
		name: "shop",
		kind: "stack",
		sourceType: "compose",
	});
	return {
		...base,
		config: {
			...base.config,
			image: undefined,
			stack: { compose, components },
		},
		...rest,
	} as Service;
}

export { makeObserved };

/** A server as the dashboard holds it; override what the case cares about. */
export function makeServer(over: Partial<Server> & { id: string }): Server {
	return {
		name: over.id,
		status: "online",
		publicIp: null,
		baseDomain: null,
		acmeEmail: null,
		agentVersion: null,
		protocolVersion: null,
		facts: null,
		lastSeenAt: null,
		createdAt: "2026-01-01T00:00:00Z",
		proxy: {
			status: "ready",
			v2: true,
			generation: 0,
			appliedGeneration: 0,
			tailnet: null,
		},
		latest: null,
		serviceCount: 0,
		drainState: "none",
		drainMessage: "",
		acceptWorkloads: true,
		buildEnabled: false,
		buildMaxConcurrent: 1,
		buildAllowEmulation: false,
		buildAllowPreviews: false,
		controlPlane: false,
		tunnel: null,
		unrestricted: false,
		composeVersion: "2.29.1",
		...over,
	};
}
