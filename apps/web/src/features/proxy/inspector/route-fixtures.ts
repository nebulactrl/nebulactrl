// Test-only builders for route services: the wire `ServiceView` the API
// returns (for stubApi) and the `Service` the components receive.

import type {
	DomainView,
	RouteConfig,
	ServerRef,
	ServiceView,
} from "#/api/gen/types.gen";
import { type Service, toService } from "#/api/models";
import { make, type Overrides } from "#/test/factories";

const NOW = "2026-10-31T12:00:00Z";

export const SERVER: ServerRef = {
	id: "srv-1",
	name: "edge-1",
	status: "online",
	lastSeenAt: NOW,
};

export function domainView(overrides: Overrides<DomainView> = {}): DomainView {
	return make("DomainView", {
		id: "dom-1",
		hostname: "app.example.com",
		targetPort: 0,
		component: "",
		process: "",
		generated: false,
		status: "active",
		statusMessage: "",
		dnsOk: null,
		dnsCheckedAt: null,
		createdAt: NOW,
		dnsResolver: "",
		cert: null,
		dns: null,
		wwwPartner: "",
		wwwRedirect: false,
		wwwRedirectFor: "",
		pathPrefix: "",
		stripPrefix: false,
		addPrefix: "",
		kind: "serve",
		redirectTo: "",
		redirectCode: 308,
		preservePath: true,
		tlsMode: "auto",
		certificateId: "",
		options: {},
		authUsers: [],
		...overrides,
	});
}

/** What a test may override: the server is null for a service not placed yet. */
export type ViewOverrides = Overrides<ServiceView>;

/** A route service (kind "route") as the API sends it. */
export function routeView(
	overrides: ViewOverrides = {},
	route: Overrides<RouteConfig> = {},
): ServiceView {
	return make("ServiceView", {
		id: "svc-route",
		environmentId: "env-1",
		projectId: "prj-1",
		name: "homeassistant",
		slug: "homeassistant",
		kind: "route",
		sourceType: "external",
		config: {
			healthcheck: {
				type: "none",
				deadlineSeconds: 0,
				intervalSeconds: 0,
				startPeriodSeconds: 0,
				successThreshold: 1,
				timeoutSeconds: 0,
			},
			imageRetention: 0,
			stopGraceSeconds: 10,
			route: {
				protocol: "http",
				scheme: "http",
				upstreams: [{ address: "192.168.1.20:8123" }],
				...route,
			},
		},
		serverId: SERVER.id,
		server: SERVER,
		generation: 7,
		appliedGeneration: 7,
		hasStagedChanges: false,
		currentDeploymentId: "",
		latestDeployment: {
			id: "",
			createdAt: NOW,
			currentStep: null,
			failedStep: null,
			finishedAt: null,
			message: null,
			releaseId: "",
			status: "",
			statusMessage: "",
			strategy: "",
			trigger: "",
		},
		currentRelease: null,
		observed: null,
		observedAt: null,
		stale: false,
		domains: [],
		privateHost: "homeassistant.internal",
		gitTokenSet: false,
		registryAuthSet: false,
		createdAt: NOW,
		updatedAt: NOW,
		desiredState: "running",
		volumes: [],
		scaling: {
			autoscaling: { enabled: false, max: 1, min: 1, targetCpuPercent: 70 },
			blockedReason: null,
			eligible: false,
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
		...overrides,
	});
}

/** The same route service as the component model. */
export function routeService(
	overrides: ViewOverrides = {},
	route: Overrides<RouteConfig> = {},
): Service {
	return toService(routeView(overrides, route));
}
