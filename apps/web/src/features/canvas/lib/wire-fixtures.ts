import type { ServiceView } from "#/api/gen/types.gen";

const WHEN = "2026-01-01T00:00:00Z";

/**
 * A complete service as the API sends it (`GET /environments/{id}/services`,
 * `GET /services/{id}`): an image app that is placed nowhere,
 * has no deployment yet, and carries every field the spec requires. Override
 * what the case cares about.
 */
export function serviceWire(over: Partial<ServiceView> = {}): ServiceView {
	return {
		id: "svc-1",
		environmentId: "env-1",
		projectId: "proj-1",
		name: "api",
		slug: "api",
		kind: "app",
		sourceType: "image",
		config: {
			image: "traefik/whoami:latest",
			healthcheck: {
				type: "none",
				intervalSeconds: 0,
				timeoutSeconds: 0,
				startPeriodSeconds: 0,
				deadlineSeconds: 0,
				successThreshold: 1,
			},
			imageRetention: 5,
			stopGraceSeconds: 30,
		},
		serverId: null,
		server: null,
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
		privateHost: "",
		gitTokenSet: false,
		registryAuthSet: false,
		createdAt: WHEN,
		updatedAt: WHEN,
		desiredState: "running",
		volumes: [],
		scaling: {
			autoscaling: { enabled: false, min: 1, max: 1, targetCpuPercent: 70 },
			blockedReason: null,
			eligible: true,
			ineligibleReason: null,
			lastScaleEvent: null,
			ready: 0,
			replicas: 1,
		},
		pendingChangeCounts: { settings: 0, variables: 0 },
		stagedKind: null,
		maintenance: {
			enabled: false,
			eta: null,
			message: "",
			since: null,
			title: "",
		},
		git: null,
		tailnet: null,
		networks: [],
		processes: [],
		...over,
	};
}
