import type { LatestDeployment, Service } from "#/api/models";

let counter = 0;

/** A minimal valid service for tests; override what the case cares about. */
export function makeService(overrides: Partial<Service> = {}): Service {
	counter += 1;
	const id = overrides.id ?? `svc-${counter}`;
	return {
		id,
		environmentId: "env-1",
		projectId: "proj-1",
		name: `Service ${counter}`,
		slug: `service-${counter}`,
		kind: "app",
		sourceType: "image",
		config: {
			image: "traefik/whoami:latest",
			port: 80,
			healthcheck: {
				type: "http",
				path: "/",
				intervalSeconds: 2,
				timeoutSeconds: 3,
				startPeriodSeconds: 0,
				deadlineSeconds: 120,
				successThreshold: 2,
			},
			stopGraceSeconds: 30,
			imageRetention: 5,
			processes: [],
		},
		serverId: "server-1",
		server: {
			id: "server-1",
			name: "hetzner-01",
			status: "online",
			lastSeenAt: null,
		},
		generation: 1,
		appliedGeneration: 1,
		hasStagedChanges: false,
		currentDeploymentId: null,
		latestDeployment: null,
		currentRelease: null,
		observed: null,
		observedAt: null,
		stale: false,
		domains: [],
		privateHost: `service-${counter}`,
		gitTokenSet: false,
		registryAuthSet: false,
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
		desiredState: "running",
		volumes: [],
		scaling: {
			replicas: 1,
			ready: 1,
			autoscaling: { enabled: false, min: 1, max: 3, targetCpuPercent: 70 },
			lastScaleEvent: null,
			blockedReason: null,
			eligible: true,
			ineligibleReason: null,
		},
		pendingChangeCounts: { variables: 0, settings: 0 },
		stagedKind: null,
		maintenance: {
			enabled: false,
			title: "",
			message: "",
			eta: null,
			since: null,
		},
		git: null,
		tailnet: null,
		networks: [],
		processes: [],
		...overrides,
	};
}

/** A valid observed state (one running, healthy replica); override what the case cares about. */
export function makeObserved(
	overrides: Partial<NonNullable<Service["observed"]>> = {},
): NonNullable<Service["observed"]> {
	return {
		containerId: "c-1",
		health: "healthy",
		imageId: "sha256:0",
		replicas: [
			{
				component: "",
				index: 0,
				process: "",
				containerId: "c-1",
				state: "running",
				health: "healthy",
				startedAt: null,
				restartCount: 0,
			},
		],
		processes: [],
		replicasDesired: 1,
		replicasReady: 1,
		startedAt: null,
		state: "running",
		...overrides,
	};
}

export function makeDeployment(
	status: LatestDeployment["status"],
	overrides: Partial<LatestDeployment> = {},
): LatestDeployment {
	return {
		id: `dep-${status}`,
		status,
		trigger: "manual",
		strategy: "rolling",
		statusMessage: null,
		createdAt: "2026-01-01T00:00:00Z",
		finishedAt: null,
		releaseId: "rel-1",
		message: null,
		failedStep: null,
		currentStep: null,
		...overrides,
	};
}
